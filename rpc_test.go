package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestSharedEndpointSelectionAndAuthentication(t *testing.T) {
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
	// An existing directory without a CLI must not block an explicit endpoint.
	missingCLI := t.TempDir()
	for _, setting := range []bool{true, false} {
		name, configured, environment := "environment", "", "  "+endpoint+"  "
		if setting {
			name, configured, environment = "setting", "  "+endpoint+"  ", "invalid://ignored"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("PENCODE_CODEX_SERVER_URL", environment)
			client, err := startClient(t.Context(), t.TempDir(), missingCLI, configured, nil)
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
		t.Skip("requires Unix sockets")
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

func TestDiscoverDefaultSocketWithoutExecutable(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("PENCODE_CODEX_SERVER_URL", "")
	linkDefaultSocket(t, unixRPCServer(t, false))
	// A directory with no executable would fail path resolution or startup.
	missingCLI := t.TempDir()
	for _, endpoint := range []string{"", "unix://", ""} {
		client, err := startClient(t.Context(), t.TempDir(), missingCLI, endpoint, nil)
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

func TestRejectedDefaultSocketDoesNotStartDaemon(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("PENCODE_CODEX_SERVER_URL", "")
	linkDefaultSocket(t, unixRPCServer(t, true))
	client, err := startClient(t.Context(), t.TempDir(), t.TempDir(), "", nil)
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
