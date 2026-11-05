package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestConnectionDiagnostics(t *testing.T) {
	var logs bytes.Buffer
	previous := codexLog.Writer()
	codexLog.SetOutput(&logs)
	t.Cleanup(func() { codexLog.SetOutput(previous) })

	path := fixtureExecutable(t)
	root := t.TempDir()
	t.Setenv("PENCODE_CODEX_SERVER_URL", "")
	t.Setenv("PENCODE_CODEX_SERVER_TOKEN", "private-bearer-token")
	t.Setenv("PENCODE_CODEX_DAEMON_SOCKET", unixRPCServer(t, false))
	t.Setenv("PENCODE_CODEX_DAEMON_LOG", filepath.Join(t.TempDir(), "starts"))
	for range 2 {
		client, err := startClient(t.Context(), root, path, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = call[json.RawMessage](client, "initialize", "private-request-body")
		if err == nil {
			err = client.notify("initialized", struct{}{})
		}
		client.close()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{
		"[Codex]", "source=auto", "cwd=" + `"` + root + `"`,
		"探测默认 app-server", "启动共享 daemon", `args=["app-server" "daemon" "start"]`,
		"共享 daemon 已就绪", "复用已有 app-server", `args=["app-server" "proxy" "--sock"`,
		"proxy 已启动: pid=", "WebSocket 握手:", "bearer=true", "WebSocket 已就绪",
		"proxy 已回收: pid=", "已关闭: context canceled",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing %q in diagnostics:\n%s", want, &logs)
		}
	}
	for _, secret := range []string{"private-bearer-token", "private-request-body"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("diagnostics contain %q", secret)
		}
	}
	if strings.Contains(logs.String(), "RPC") {
		t.Errorf("successful RPC calls produced diagnostics:\n%s", &logs)
	}

	logs.Reset()
	client, err := startClient(t.Context(), root, filepath.Join(root, "missing-codex"), "unix://", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = call[json.RawMessage](client, "initialize", struct{}{})
	client.close()
	if err == nil {
		t.Fatal("missing executable unexpectedly connected")
	}
	for _, want := range []string{"启动进程:", "missing-codex", "失败或断开:", "RPC 失败: method=initialize", "启动 Codex proxy"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing %q in failure diagnostics:\n%s", want, &logs)
		}
	}
}

func TestLogEndpointOmitsCredentials(t *testing.T) {
	endpoint := logEndpoint("wss://user:private-password@localhost:4500/rpc?token=private-token#private-fragment")
	if endpoint != "wss://localhost:4500/rpc" {
		t.Fatalf("unexpected diagnostic endpoint: %q", endpoint)
	}
}
