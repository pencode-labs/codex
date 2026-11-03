package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pencode-labs/pencode/peb"
)

func TestLiveIndependentServerObservation(t *testing.T) {
	for _, stop := range []bool{false, true} {
		name := "completed"
		if stop {
			name = "interrupted"
		}
		t.Run(name, func(t *testing.T) { testLiveIndependentServerObservation(t, stop) })
	}
}

func testLiveIndependentServerObservation(t *testing.T, stop bool) {
	liveCodexHome(t)
	release := make(chan struct{})
	requested := make(chan struct{}, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(value any) {
			data, _ := json.Marshal(value)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		}
		emit(map[string]any{"type": "response.created", "response": map[string]any{"id": "local", "status": "in_progress", "output": []any{}}})
		emit(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "message", "id": "progress", "role": "assistant", "phase": "commentary", "content": []map[string]string{{"type": "output_text", "text": "External progress"}}}})
		requested <- struct{}{}
		select {
		case <-release:
			emit(map[string]any{"type": "response.output_item.done", "output_index": 1, "item": map[string]any{"type": "message", "id": "reply", "role": "assistant", "phase": "final_answer", "content": []map[string]string{{"type": "output_text", "text": "External reply"}}}})
			emit(map[string]any{"type": "response.completed", "response": map[string]any{"id": "local", "status": "completed", "output": []any{}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}})
		case <-r.Context().Done():
		}
	}))
	defer provider.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	config := fmt.Sprintf("model = %q\nmodel_provider = %q\n[model_providers.local_fixture]\nname = %q\nbase_url = %q\nwire_api = %q\nrequires_openai_auth = false\nsupports_websockets = false\n", "gpt-6.1-sol", "local_fixture", "Local fixture", provider.URL, "responses")
	if err := os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	a := &agent{ctx: t.Context(), context: peb.ActionContext{ProjectPath: root}, conversations: make(map[string]*conversation)}
	defer a.closeClient()
	c, err := a.startThread()
	if err != nil {
		t.Fatal(err)
	}
	started, err := call[struct {
		Turn turn `json:"turn"`
	}](a.client, "turn/start", map[string]any{"threadId": c.ID, "input": []input{{Type: "text", Text: "External fixture"}}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-requested:
	case <-time.After(8 * time.Second):
		t.Fatal("local provider was not reached")
	}
	socket := filepath.Join(os.Getenv("CODEX_HOME"), "observer.sock")
	cmd := exec.CommandContext(t.Context(), os.Getenv("PENCODE_CODEX_PATH"), "app-server", "--listen", "unix://"+socket)
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	deadline := time.Now().Add(8 * time.Second)
	for {
		if _, err = os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("observer socket not created", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	b := &agent{ctx: t.Context(), context: a.context, serverURL: "unix://" + socket, conversations: make(map[string]*conversation), workspaceOpen: true}
	defer b.closeClient()
	if err = b.resume(c.ID); err != nil {
		t.Fatal(err)
	}
	observed := b.selected
	if observed.loaded || !observed.observing || !observed.busy || observed.turnID != started.Turn.ID {
		t.Fatal("independent writer did not restore observed activity", observed)
	}
	for !strings.Contains(b.composerView().HTML, "External progress") {
		if time.Now().After(deadline) {
			t.Fatal("running content was not synchronized")
		}
		if err = b.syncConversation(observed); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	next := b.client.next
	if err = b.interrupt(observed); err != nil || b.client.next != next {
		t.Fatal("observer attempted to interrupt another server", err)
	}
	if err = b.closeWorkspace(); err != nil {
		t.Fatal(err)
	}
	if stop {
		if _, err = call[json.RawMessage](a.client, "turn/interrupt", map[string]string{"threadId": c.ID, "turnId": started.Turn.ID}); err != nil {
			t.Fatal(err)
		}
	} else {
		close(release)
	}
	for time.Now().Before(deadline) {
		if err = b.syncConversation(observed); err != nil {
			t.Fatal(err)
		}
		if !observed.busy {
			if observed.unread == stop || observed.progress == nil || !observed.progress.complete {
				t.Fatal("terminal state or unread result is incorrect", observed)
			}
			if !stop && !strings.Contains(b.composerView().HTML, "External reply") {
				t.Fatal("completed reply missing")
			}
			if err = b.openConversation(c.ID); err != nil {
				t.Fatal(err)
			}
			if observed.unread {
				t.Fatal("viewing did not clear unread")
			}
			t.Log("independent app-server synchronized running content and terminal state without taking writer ownership; local mock inference only")
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("terminal state was not synchronized", observed)
}
