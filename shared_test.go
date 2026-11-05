package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pencode-labs/pencode/peb"
)

// A single backend holds the active turn across connections and replays its
// pending approval before thread/resume responds, as a shared server can do.
func TestSharedSessionSurvivesClientCloseAndReconnect(t *testing.T) {
	for _, transport := range []string{"websocket", "proxy"} {
		t.Run(transport, func(t *testing.T) { testSharedSessionSurvivesClientCloseAndReconnect(t, transport) })
	}
}

func testSharedSessionSurvivesClientCloseAndReconnect(t *testing.T, transport string) {
	ctx := t.Context()
	var mutex sync.Mutex
	connections := make(map[*websocket.Conn]bool)
	running, approved := true, false
	interrupts, starts := 0, 0
	send := func(conn *websocket.Conn, value any) error {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return conn.Write(ctx, websocket.MessageText, data)
	}
	event := func(method string, params any) envelope {
		data, _ := json.Marshal(params)
		return envelope{Method: method, Params: data}
	}
	broadcast := func(message envelope) {
		for conn, subscribed := range connections {
			if subscribed {
				_ = send(conn, message)
			}
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		defer func() { mutex.Lock(); delete(connections, conn); mutex.Unlock() }()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var request envelope
			if json.Unmarshal(data, &request) != nil {
				return
			}
			mutex.Lock()
			result := any(struct{}{})
			switch request.Method {
			case "initialized":
				mutex.Unlock()
				continue
			case "initialize":
			case "thread/resume":
				connections[conn] = true
				result = map[string]any{"thread": map[string]any{"id": "shared", "name": "Shared task"}}
				if running {
					_ = send(conn, event("turn/started", map[string]any{"threadId": "shared", "turn": turn{ID: "turn", Status: "inProgress"}}))
					_ = send(conn, event("item/agentMessage/delta", map[string]string{"threadId": "shared", "itemId": "reply", "delta": "live reply"}))
					if !approved {
						_ = send(conn, envelope{ID: json.RawMessage(`"approval"`), Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{"threadId":"shared","turnId":"turn","command":"test"}`)})
					}
				}
			case "thread/turns/list":
				status := "completed"
				if running {
					status = "inProgress"
				}
				result = map[string]any{"data": []turn{{ID: "turn", Status: status, Items: []threadItem{{ID: "user", Type: "userMessage", Content: []input{{Type: "text", Text: "original task"}}}}}}}
			case "thread/loaded/list":
				result = map[string]any{"data": []string{"shared"}}
			case "turn/interrupt":
				interrupts++
				running = false
				broadcast(event("turn/completed", map[string]any{"threadId": "shared", "turn": turn{ID: "turn", Status: "interrupted"}}))
			case "turn/start":
				starts++
				running = true
				result = map[string]any{"turn": turn{ID: "next", Status: "inProgress"}}
				broadcast(event("turn/started", map[string]any{"threadId": "shared", "turn": turn{ID: "next", Status: "inProgress"}}))
			case "":
				approved = true
				broadcast(event("serverRequest/resolved", map[string]any{"threadId": "shared", "requestId": "approval"}))
				mutex.Unlock()
				continue
			default:
				_ = send(conn, envelope{ID: request.ID, Error: &rpcError{Code: -32601, Message: request.Method}})
				mutex.Unlock()
				continue
			}
			payload, _ := json.Marshal(result)
			err = send(conn, envelope{ID: request.ID, Result: payload})
			mutex.Unlock()
			if err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	endpoint := "ws" + strings.TrimPrefix(server.URL, "http")
	if transport == "proxy" {
		fixtureExecutable(t)
		t.Setenv("PENCODE_CODEX_PROXY_URL", endpoint)
		endpoint = "unix://"
	}
	root := t.TempDir()
	open := func() *agent {
		a := &agent{ctx: ctx, serverURL: endpoint, context: peb.ActionContext{ProjectPath: root}, conversations: make(map[string]*conversation), workspaceOpen: true}
		t.Cleanup(a.closeClient)
		if err := a.resume("shared"); err != nil {
			t.Fatal(err)
		}
		return a
	}
	syncEvents := func(a *agent) {
		t.Helper()
		if _, err := call[json.RawMessage](a.client, "thread/loaded/list", struct{}{}); err != nil {
			t.Fatal(err)
		}
	}
	a, b := open(), open()
	for _, client := range []*agent{a, b} {
		if !client.selected.loaded || !client.selected.busy || client.selected.turnID != "turn" || len(client.selected.requests) != 1 {
			t.Fatal("resume lost the running turn or replayed approval", client.selected)
		}
		if !strings.Contains(client.composerView().HTML, "live reply") || !strings.Contains(client.composerView().HTML, "original task") {
			t.Fatal("history hydration erased replayed events", client.composerView().HTML)
		}
	}
	// Another client resolves the approval, and both views remove it.
	if err := b.answer(b.selected, `"approval"`, "accept"); err != nil {
		t.Fatal(err)
	}
	syncEvents(b)
	syncEvents(a)
	if len(a.selected.requests) != 0 || len(b.selected.requests) != 0 {
		t.Fatal("approval resolution did not reach both clients")
	}
	a.selected.draft = "draft survives"
	if err := a.closeWorkspace(); err != nil {
		t.Fatal(err)
	}
	a.closeClient()
	syncEvents(b)
	mutex.Lock()
	stillRunning := running
	stopped := interrupts
	mutex.Unlock()
	if !stillRunning || stopped != 0 || !b.selected.busy {
		t.Fatal("closing one client stopped shared work")
	}
	a.workspaceOpen = true
	if err := a.resume("shared"); err != nil {
		t.Fatal(err)
	}
	if a.selected.draft != "draft survives" || !a.selected.busy || len(a.selected.requests) != 0 {
		t.Fatal("reconnect lost state or revived resolved approval")
	}
	// Explicit stop affects the common turn and is delivered to the other view.
	if err := b.interrupt(b.selected); err != nil {
		t.Fatal(err)
	}
	syncEvents(a)
	syncEvents(b)
	if a.selected.busy || b.selected.busy {
		t.Fatal("explicit stop did not reach both views")
	}
	b.selected.draft = "continue"
	var published capturedUI
	b.publisher = &published
	if err := b.send(); err != nil {
		t.Fatal(err)
	}
	syncEvents(a)
	mutex.Lock()
	started, stopped := starts, interrupts
	mutex.Unlock()
	if started != 1 || stopped != 1 || !a.selected.busy || !b.selected.busy {
		t.Fatal("continued turn did not use the shared backend")
	}
}

// Real CLI coverage exercises separate proxies sharing one server and writer
// ownership. Injecting an item materializes history without inference.
func TestLiveSharedAppServer(t *testing.T) {
	liveCodexHome(t)
	root := t.TempDir()
	liveAppServer(t, root, "")
	first, err := startClient(t.Context(), root, "", "unix://", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.close()
	if _, err = call[json.RawMessage](first, "initialize", map[string]any{"clientInfo": map[string]string{"name": "pencode_shared_test", "version": "0.0.1"}, "capabilities": map[string]bool{"experimentalApi": true}}); err != nil {
		t.Fatal(err)
	}
	if err = first.notify("initialized", struct{}{}); err != nil {
		t.Fatal(err)
	}
	started, err := call[struct {
		Thread thread `json:"thread"`
	}](first, "thread/start", map[string]any{"cwd": root, "sandbox": "read-only", "approvalPolicy": "never", "excludeTurns": true})
	if err != nil {
		t.Fatal(err)
	}
	id := started.Thread.ID
	if _, err = call[json.RawMessage](first, "thread/inject_items", map[string]any{"threadId": id, "items": []map[string]any{{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": "Local persistence probe; no inference."}}}}}); err != nil {
		t.Fatal(err)
	}
	second := &agent{ctx: t.Context(), context: peb.ActionContext{ProjectPath: root}, conversations: make(map[string]*conversation), workspaceOpen: true}
	defer second.closeClient()
	if err = second.resume(id); err != nil {
		t.Fatal("second client could not rejoin the shared writer", err)
	}
	if !second.selected.loaded {
		t.Fatal("shared conversation became read-only")
	}
	first.close()
	if _, err = call[json.RawMessage](second.client, "thread/loaded/list", struct{}{}); err != nil {
		t.Fatal("closing first client stopped the daemon", err)
	}
	second.selected.draft = "keep draft"
	if err = second.closeWorkspace(); err != nil {
		t.Fatal(err)
	}
	second.closeClient()
	second.workspaceOpen = true
	if err = second.resume(id); err != nil || !second.selected.loaded || second.selected.draft != "keep draft" {
		t.Fatal("new connection could not resume the same session", err)
	}
	t.Log("two clients discovered the same existing server and reconnected after closing; no inference requested")
}

func TestLiveSharedRunningTurn(t *testing.T) {
	liveCodexHome(t)
	release := make(chan struct{})
	requested := make(chan struct{}, 1)
	inferenceEnded := make(chan struct{}, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(value any) {
			data, _ := json.Marshal(value)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		}
		emit(map[string]any{"type": "response.created", "response": map[string]any{"id": "local-response", "status": "in_progress", "output": []any{}}})
		requested <- struct{}{}
		select {
		case <-release:
			emit(map[string]any{"type": "response.completed", "response": map[string]any{"id": "local-response", "status": "completed", "output": []any{}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}})
		case <-r.Context().Done():
		}
		inferenceEnded <- struct{}{}
	}))
	defer func() { close(release); provider.Close() }()
	config := fmt.Sprintf("model = %q\nmodel_provider = %q\n[model_providers.local_fixture]\nname = %q\nbase_url = %q\nwire_api = %q\nrequires_openai_auth = false\nsupports_websockets = false\n", "gpt-6.1-sol", "local_fixture", "Local fixture", provider.URL, "responses")
	if err := os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	liveAppServer(t, root, "")
	a := &agent{ctx: t.Context(), context: peb.ActionContext{ProjectPath: root}, conversations: make(map[string]*conversation), workspaceOpen: true}
	defer a.closeClient()
	c, err := a.startThread()
	if err != nil {
		t.Fatal(err)
	}
	started, err := call[struct {
		Turn turn `json:"turn"`
	}](a.client, "turn/start", map[string]any{"threadId": c.ID, "input": []input{{Type: "text", Text: "local fixture"}}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-requested:
	case <-time.After(8 * time.Second):
		t.Fatal("local provider was not reached")
	}
	b := &agent{ctx: t.Context(), context: a.context, conversations: make(map[string]*conversation), workspaceOpen: true}
	defer b.closeClient()
	if err = b.resume(c.ID); err != nil {
		t.Fatal(err)
	}
	if !b.selected.loaded || !b.selected.busy || b.selected.turnID != started.Turn.ID {
		t.Fatalf("live resume did not restore active turn: busy=%v turn=%q want=%q", b.selected.busy, b.selected.turnID, started.Turn.ID)
	}
	if err = a.closeWorkspace(); err != nil {
		t.Fatal(err)
	}
	a.closeClient()
	select {
	case <-inferenceEnded:
		t.Fatal("closing the first view stopped inference")
	default:
	}
	if err = b.interrupt(b.selected); err != nil {
		t.Fatal(err)
	}
	select {
	case <-inferenceEnded:
	case <-time.After(5 * time.Second):
		t.Fatal("explicit stop did not cancel the local provider request")
	}
	t.Log("shared daemon retained an active local mock turn across view close; explicit stop cancelled it; no OpenAI request")
}
