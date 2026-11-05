package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

func fixtureDaemonStart() error {
	socket := os.Getenv("PENCODE_CODEX_DAEMON_SOCKET")
	endpoint, err := defaultServerURL("")
	if err != nil {
		return err
	}
	control := strings.TrimPrefix(endpoint, "unix://")
	if err := os.MkdirAll(filepath.Dir(control), 0700); err != nil {
		return err
	}
	_ = os.Remove(control)
	if err := os.Symlink(socket, control); err != nil {
		return err
	}
	log, err := os.OpenFile(os.Getenv("PENCODE_CODEX_DAEMON_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	if _, err := io.WriteString(log, "start\n"); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"socketPath": socket})
}

// Existing protocol fixtures run behind a real WebSocket handshake and framing.
func fixtureExecutable(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PENCODE_CODEX_PATH", path)
	t.Setenv("PENCODE_CODEX_FIXTURE", "1")
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	server, closeServer := fixtureWebSocket(t.Context(), path)
	t.Cleanup(closeServer)
	t.Setenv("PENCODE_CODEX_PROXY_URL", "ws"+strings.TrimPrefix(server.URL, "http"))
	t.Setenv("PENCODE_CODEX_SERVER_URL", "unix://")
	return path
}

func fixtureProxy() error {
	ctx := context.Background()
	endpoint := os.Getenv("PENCODE_CODEX_PROXY_URL")
	var conn net.Conn
	var err error
	if len(os.Args) == 5 && os.Args[3] == "--sock" {
		socket, err := filepath.EvalSymlinks(os.Args[4])
		if err != nil {
			return err
		}
		conn, err = (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if err != nil {
			return err
		}
	} else if len(os.Args) == 3 && strings.HasPrefix(endpoint, "ws://") {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", strings.TrimPrefix(endpoint, "ws://"))
		if err != nil {
			return err
		}
	} else {
		return fmt.Errorf("invalid proxy invocation: %q", os.Args)
	}
	defer conn.Close()
	if logPath := os.Getenv("PENCODE_CODEX_PROXY_LOG"); logPath != "" {
		log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		err = json.NewEncoder(log).Encode(os.Getpid())
		_ = log.Close()
		if err != nil {
			return err
		}
	}
	go func() { _, _ = io.Copy(conn, os.Stdin); _ = conn.Close() }()
	_, err = io.Copy(os.Stdout, conn)
	return err
}

func fixtureWebSocket(parent context.Context, path string) (*httptest.Server, func()) {
	ctx, cancelServer := context.WithCancel(parent)
	var workers sync.WaitGroup
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workers.Add(1)
		defer workers.Done()
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(8 << 20)
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, "app-server")
		input, err := cmd.StdinPipe()
		if err != nil {
			return
		}
		defer input.Close()
		output, err := cmd.StdoutPipe()
		if err != nil {
			return
		}
		if err := cmd.Start(); err != nil {
			return
		}
		readerDone := make(chan struct{})
		go func() {
			defer close(readerDone)
			defer cancel()
			for {
				kind, data, err := conn.Read(ctx)
				if err != nil || kind != websocket.MessageText {
					return
				}
				if _, err := input.Write(data); err != nil {
					return
				}
				if len(data) == 0 || data[len(data)-1] != '\n' {
					if _, err := io.WriteString(input, "\n"); err != nil {
						return
					}
				}
			}
		}()
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 64<<10), 8<<20)
		for scanner.Scan() {
			if err := conn.Write(ctx, websocket.MessageText, scanner.Bytes()); err != nil {
				break
			}
		}
		cancel()
		_ = conn.CloseNow()
		_ = cmd.Wait()
		<-readerDone
	}))
	return server, func() {
		server.Close()
		cancelServer()
		workers.Wait()
	}
}
