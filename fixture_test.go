package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

// Existing protocol fixtures run behind a real WebSocket handshake and framing.
func fixtureExecutable(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PENCODE_CODEX_PATH", path)
	t.Setenv("PENCODE_CODEX_FIXTURE", "1")
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	server, closeServer := fixtureWebSocket(t.Context(), path)
	t.Cleanup(closeServer)
	t.Setenv("PENCODE_CODEX_SERVER_URL", "ws"+strings.TrimPrefix(server.URL, "http"))
	return path
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
