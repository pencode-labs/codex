package main

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func liveCodexHome(t *testing.T) {
	t.Helper()
	if os.Getenv("PENCODE_CODEX_INTEGRATION") != "1" {
		t.Skip("set PENCODE_CODEX_INTEGRATION=1 for a local CLI smoke test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("local shared server tests require Unix sockets")
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	// Keep the Unix socket path short enough for sockaddr_un.
	directory, err := os.MkdirTemp("", "pcodex-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", directory)
	t.Setenv("PENCODE_CODEX_PATH", path)
	t.Setenv("PENCODE_CODEX_SERVER_URL", "")
	t.Setenv("PENCODE_CODEX_SERVER_TOKEN", "")
	t.Cleanup(func() {
		_ = os.RemoveAll(directory)
	})
}

// A foreground server has one owned process to cancel and reap. Managed daemon
// installation also starts durable updater helpers that daemon stop can leave
// behind after an isolated CODEX_HOME has been deleted.
func liveAppServer(t *testing.T, root, endpoint string) string {
	t.Helper()
	if endpoint == "" {
		var err error
		endpoint, err = defaultServerURL(root)
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cmd := exec.CommandContext(ctx, os.Getenv("PENCODE_CODEX_PATH"), "app-server", "--listen", endpoint)
	cmd.Dir, cmd.Stderr, cmd.WaitDelay = root, os.Stderr, time.Second
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })
	socket := strings.TrimPrefix(endpoint, "unix://")
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			return endpoint
		}
		if time.Now().After(deadline) {
			t.Fatal("app-server socket not created", socket)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
