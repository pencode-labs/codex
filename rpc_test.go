package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
