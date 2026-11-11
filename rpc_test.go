package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestSharedEndpointSelectionAndAuthentication(t *testing.T) {
	for _, proxy := range []bool{false, true} {
		name := "websocket"
		if proxy {
			name = "proxy"
		}
		t.Run(name, func(t *testing.T) { testSharedEndpointSelectionAndAuthentication(t, proxy) })
	}
}

func testSharedEndpointSelectionAndAuthentication(t *testing.T, proxy bool) {
	const token = "local-test-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_, data, err := conn.Read(t.Context())
		if err != nil {
			return
		}
		var request envelope
		if err := json.Unmarshal(data, &request); err != nil || request.Method != "initialize" {
			t.Errorf("invalid handshake: %s, %v", data, err)
			return
		}
		reply, _ := json.Marshal(envelope{ID: request.ID, Result: json.RawMessage(`{}`)})
		_ = conn.Write(t.Context(), websocket.MessageText, reply)
		_, _, _ = conn.Read(t.Context())
	}))
	defer server.Close()
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http")
	t.Setenv("PENCODE_CODEX_SERVER_TOKEN", token)
	// Direct WebSocket addresses work without a CLI; Unix endpoints own a proxy.
	path := t.TempDir()
	if proxy {
		path = fixtureExecutable(t)
		t.Setenv("PENCODE_CODEX_PROXY_URL", endpoint)
		endpoint = "unix://"
	}
	for _, setting := range []bool{true, false} {
		name, configured, environment := "environment", "", "  "+endpoint+"  "
		if setting {
			name, configured, environment = "setting", "  "+endpoint+"  ", "invalid://ignored"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("PENCODE_CODEX_SERVER_URL", environment)
			client, err := startClient(t.Context(), t.TempDir(), path, configured, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.close()
			if _, err := call[json.RawMessage](client, "initialize", struct{}{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func unixRPCServer(t *testing.T, reject bool) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("requires socket symlinks")
	}
	// The home/control path may be longer than sockaddr_un; Codex also uses a
	// symlink to a shorter socket in that case.
	directory, err := os.MkdirTemp("", "pc-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socket := filepath.Join(directory, "rpc.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reject {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, data, err := conn.Read(t.Context())
			if err != nil {
				return
			}
			var request envelope
			if json.Unmarshal(data, &request) != nil {
				return
			}
			reply, _ := json.Marshal(envelope{ID: request.ID, Result: json.RawMessage(`{}`)})
			if conn.Write(t.Context(), websocket.MessageText, reply) != nil {
				return
			}
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); _ = listener.Close() })
	return socket
}

func linkDefaultSocket(t *testing.T, socket string) {
	t.Helper()
	endpoint, err := defaultServerURL(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	control := strings.TrimPrefix(endpoint, "unix://")
	if err = os.MkdirAll(filepath.Dir(control), 0700); err == nil {
		err = os.Symlink(socket, control)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestProxyReusesDefaultSocket(t *testing.T) {
	path := fixtureExecutable(t)
	t.Setenv("PENCODE_CODEX_SERVER_URL", "")
	linkDefaultSocket(t, unixRPCServer(t, false))
	for _, endpoint := range []string{"", "unix://", ""} {
		if endpoint == "unix://" {
			// Explicit Unix endpoints use proxy --sock to select the server.
			endpoint, _ = defaultServerURL("")
		}
		client, err := startClient(t.Context(), t.TempDir(), path, endpoint, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = call[json.RawMessage](client, "initialize", struct{}{}); err != nil {
			client.close()
			t.Fatal("existing socket was not reused", err)
		}
		client.close()
	}
}

func TestProxyProcessesAreOwnedAndReaped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process liveness probe requires Unix signals")
	}
	path := fixtureExecutable(t)
	logPath := filepath.Join(t.TempDir(), "proxies")
	t.Setenv("PENCODE_CODEX_PROXY_LOG", logPath)
	clients := make([]*rpcClient, 2)
	for i := range clients {
		client, err := startClient(t.Context(), t.TempDir(), path, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(client.close)
		clients[i] = client
		if _, err := call[json.RawMessage](client, "initialize", struct{}{}); err != nil {
			t.Fatal(err)
		}
	}
	log, err := os.Open(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	decoder := json.NewDecoder(log)
	var pids [2]int
	for i := range pids {
		if err := decoder.Decode(&pids[i]); err != nil {
			t.Fatal(err)
		}
	}
	if pids[0] == pids[1] {
		t.Fatal("clients shared a proxy process")
	}
	for i, pid := range pids {
		process, err := os.FindProcess(pid)
		if err != nil {
			t.Fatal(err)
		}
		defer process.Release()
		if i == 1 {
			if err := process.Kill(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-clients[i].done:
			case <-time.After(2 * time.Second):
				t.Fatal("proxy exit did not disconnect the client")
			}
			if clients[i].err == nil || !strings.Contains(clients[i].err.Error(), "proxy") {
				t.Fatal("proxy exit was not reported", clients[i].err)
			}
		}
		clients[i].close()
		if err := process.Signal(syscall.Signal(0)); !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("proxy %d was not reaped: %v", pid, err)
		}
		if i == 0 {
			if _, err := call[json.RawMessage](clients[1], "initialize", struct{}{}); err != nil {
				t.Fatal("closing one proxy disconnected another", err)
			}
		}
	}
}

func TestProxyCanCloseWhileHandshaking(t *testing.T) {
	path := fixtureExecutable(t)
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	t.Setenv("PENCODE_CODEX_PROXY_URL", "ws"+strings.TrimPrefix(server.URL, "http"))
	client, err := startClient(t.Context(), t.TempDir(), path, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.close)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("proxy did not forward the WebSocket handshake")
	}
	closed := make(chan struct{})
	go func() { client.close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("closing waited for the WebSocket handshake")
	}
}

func TestRejectedDefaultSocketDoesNotStartDaemon(t *testing.T) {
	path := fixtureExecutable(t)
	t.Setenv("PENCODE_CODEX_SERVER_URL", "")
	linkDefaultSocket(t, unixRPCServer(t, true))
	client, err := startClient(t.Context(), t.TempDir(), path, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.close()
	if _, err = call[json.RawMessage](client, "initialize", struct{}{}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatal("rejected socket triggered startup or hid its error", err)
	}
}

func TestAbsentDefaultSocketStartsOnceThenReuses(t *testing.T) {
	for _, stale := range []bool{false, true} {
		name := "absent"
		if stale {
			name = "stale"
		}
		t.Run(name, func(t *testing.T) {
			socket := unixRPCServer(t, false)
			path := fixtureExecutable(t)
			t.Setenv("PENCODE_CODEX_SERVER_URL", "")
			t.Setenv("PENCODE_CODEX_DAEMON_SOCKET", socket)
			log := filepath.Join(t.TempDir(), "starts")
			t.Setenv("PENCODE_CODEX_DAEMON_LOG", log)
			if stale {
				// Leave a real socket inode with no listener (ECONNREFUSED).
				listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(filepath.Dir(socket), "stale.sock"), Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				listener.SetUnlinkOnClose(false)
				_ = listener.Close()
				linkDefaultSocket(t, listener.Addr().String())
			}
			for range 3 {
				client, err := startClient(t.Context(), t.TempDir(), path, "", nil)
				if err != nil {
					t.Fatal(err)
				}
				_, err = call[json.RawMessage](client, "initialize", struct{}{})
				client.close()
				if err != nil {
					t.Fatal(err)
				}
			}
			data, err := os.ReadFile(log)
			if err != nil || string(data) != "start\n" {
				t.Fatalf("daemon startup repeated: %q, %v", data, err)
			}
		})
	}
}
