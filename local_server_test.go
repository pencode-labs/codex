package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLiveLocalServerStartup(t *testing.T) {
	if os.Getenv("PENCODE_CODEX_INTEGRATION") != "1" {
		t.Skip("set PENCODE_CODEX_INTEGRATION=1 for a local CLI smoke test")
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.MkdirTemp("", "pcl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("CODEX_HOME", home)
	t.Setenv("PENCODE_CODEX_SERVER_URL", "")
	t.Setenv("PENCODE_CODEX_SERVER_TOKEN", "")
	var logs bytes.Buffer
	writer := codexLog.Writer()
	codexLog.SetOutput(&logs)
	t.Cleanup(func() {
		codexLog.SetOutput(writer)
		t.Log(logs.String())
		_, value, ok := strings.Cut(logs.String(), "直接启动共享 app-server: pid=")
		if ok {
			pid, _ := strconv.Atoi(strings.Fields(value)[0])
			if process, err := os.FindProcess(pid); err == nil {
				_ = process.Kill()
				_, _ = process.Wait()
				_ = process.Release()
			}
		}
	})
	if _, err := proxySocket(t.Context(), home, path, ""); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		client, err := startClient(t.Context(), home, path, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = call[json.RawMessage](client, "initialize", map[string]any{"clientInfo": map[string]string{"name": "pencode_smoke", "version": "0.1.0"}})
		client.close()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func localServerFixture(t *testing.T) (string, string) {
	t.Helper()
	path := fixtureExecutable(t)
	// Windows sockaddr_un has a 108-byte limit too.
	home, err := os.MkdirTemp("", "pcl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("CODEX_HOME", home)
	t.Setenv("PENCODE_CODEX_SERVER_URL", "")
	t.Setenv("PENCODE_CODEX_DAEMON_ERROR", "Error: this CLI has no complete local package; install a packaged Codex CLI or use the standalone installer")
	pidFile := filepath.Join(home, "server.pid")
	t.Setenv("PENCODE_CODEX_LISTEN_PID", pidFile)
	t.Cleanup(func() {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			pid, _ := strconv.Atoi(string(data))
			if process, err := os.FindProcess(pid); err == nil {
				_ = process.Kill()
				_, _ = process.Wait()
				_ = process.Release()
			}
		}
	})
	return path, pidFile
}

func TestIncompletePackageStartsSharedServer(t *testing.T) {
	t.Run("absent", func(t *testing.T) { testIncompletePackageStartsSharedServer(t, false) })
	t.Run("stale", func(t *testing.T) { testIncompletePackageStartsSharedServer(t, true) })
}

func testIncompletePackageStartsSharedServer(t *testing.T, stale bool) {
	path, pidFile := localServerFixture(t)
	if stale {
		endpoint, err := defaultServerURL("")
		if err != nil {
			t.Fatal(err)
		}
		socket := strings.TrimPrefix(endpoint, "unix://")
		if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
			t.Fatal(err)
		}
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		listener.SetUnlinkOnClose(false)
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
	}
	var firstPID string
	for range 2 {
		client, err := startClient(t.Context(), t.TempDir(), path, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = call[json.RawMessage](client, "initialize", struct{}{})
		client.close()
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(pidFile)
		if err != nil || len(data) == 0 {
			t.Fatalf("missing shared server PID: %q, %v", data, err)
		}
		if firstPID != "" && string(data) != firstPID {
			t.Fatalf("reconnect replaced shared server %s with %s", firstPID, data)
		}
		firstPID = string(data)
	}
}

func TestLocalServerCancelledBeforeStartup(t *testing.T) {
	path, pidFile := localServerFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	endpoint, _ := defaultServerURL("")
	if _, err := startLocalServer(ctx, filepath.Dir(pidFile), path, endpoint); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled startup: %v", err)
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("canceled startup launched a process: %v", err)
	}
}

func TestLocalServerStartupReportsFailure(t *testing.T) {
	path, _ := localServerFixture(t)
	t.Setenv("PENCODE_CODEX_LISTEN_ERROR", "fixture listener failed")
	_, err := proxySocket(t.Context(), t.TempDir(), path, "")
	if err == nil || !strings.Contains(err.Error(), "fixture listener failed") {
		t.Fatalf("missing listener diagnostic: %v", err)
	}
}

func TestDaemonFailurePreservesDiagnostic(t *testing.T) {
	path, _ := localServerFixture(t)
	t.Setenv("PENCODE_CODEX_DAEMON_ERROR", "permission denied")
	_, err := proxySocket(t.Context(), t.TempDir(), path, "")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("missing daemon diagnostic or exit cause: %v", err)
	}
}
