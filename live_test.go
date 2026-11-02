package main

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func liveCodexHome(t *testing.T) {
	t.Helper()
	if os.Getenv("PENCODE_CODEX_INTEGRATION") != "1" {
		t.Skip("set PENCODE_CODEX_INTEGRATION=1 for a local CLI smoke test")
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
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if output, err := exec.CommandContext(ctx, path, "app-server", "daemon", "stop").CombinedOutput(); err != nil {
			t.Errorf("stop isolated daemon: %v\n%s", err, output)
		}
		_ = os.RemoveAll(directory)
	})
}
