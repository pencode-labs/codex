package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pencode-labs/pencode/peb"
)

func TestCloseWorkspaceInterruptsAfterPendingTurnStart(t *testing.T) {
	var output bytes.Buffer
	c := &conversation{thread: thread{ID: "thread"}, busy: true, loaded: true}
	a := &agent{selected: c, workspaceOpen: true, commands: make(chan command, 1), conversations: map[string]*conversation{c.ID: c}}
	client := &rpcClient{ctx: t.Context(), writer: json.NewEncoder(&output), messages: make(chan envelope, 2), commands: a.commands, handle: a.onMessage}
	client.done = make(chan struct{})
	client.cancel = func() { close(client.done) }
	client.stdin = closeWriter{&output}
	a.client = client
	client.dispatch = func(cmd command) {
		a.handlePendingCommand(cmd)
		client.messages <- envelope{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"turn":{"id":"turn","status":"inProgress"}}`)}
	}
	a.commands <- command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "close"}, Reason: "button"}}
	result, err := call[struct {
		Turn turn `json:"turn"`
	}](client, "turn/start", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if a.workspaceOpen || a.selected != nil || !a.exitsPending || !c.exitRequested || strings.Contains(output.String(), "turn/interrupt") {
		t.Fatal("closing must exit immediately and defer the interrupt until turn/start returns", output.String())
	}
	c.turnID = result.Turn.ID
	client.messages <- envelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"interrupted"}}`)}
	client.messages <- envelope{ID: json.RawMessage(`2`), Result: json.RawMessage(`{}`)}
	a.exitConversations()
	if !strings.Contains(output.String(), `"method":"turn/interrupt"`) || !strings.Contains(output.String(), `"turnId":"turn"`) || c.busy || c.loaded || a.exitsPending || c.exitRequested || a.client != nil {
		t.Fatal("close did not interrupt the right turn or release the conversation", output.String())
	}
	if a.conversations[c.ID] != c {
		t.Fatal("closing deleted conversation history")
	}
}

func TestCloseWorkspaceLeavesIdleConversationInHistory(t *testing.T) {
	c := &conversation{thread: thread{ID: "thread"}, loaded: true, items: []*chatItem{newChatItem(threadItem{Type: "agentMessage", Text: "reply"}, true)}}
	a := &agent{selected: c, workspaceOpen: true, conversations: map[string]*conversation{c.ID: c}}
	if err := a.closeWorkspace(); err != nil {
		t.Fatal(err)
	}
	if a.selected != nil || a.workspaceOpen || c.loaded || c.items != nil || !a.exitsPending || a.conversations[c.ID] != c {
		t.Fatal("closing an idle conversation must exit while keeping history")
	}
	a.exitConversations()
	if a.exitsPending || c.exitRequested {
		t.Fatal("idle conversation was not released")
	}
}

type closeWriter struct{ io.Writer }

func (closeWriter) Close() error { return nil }

func TestAppServerShutdownDrainsOutput(t *testing.T) {
	path := fixtureExecutable(t)
	t.Setenv("PENCODE_CODEX_SHUTDOWN_OUTPUT", "1")
	for _, cancelParent := range []bool{false, true} {
		t.Run(map[bool]string{false: "close", true: "extension shutdown"}[cancelParent], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			client, err := startClient(ctx, t.TempDir(), path, nil)
			if err != nil {
				t.Fatal(err)
			}
			if cancelParent {
				cancel()
			}
			client.close()
			if !client.cmd.ProcessState.Success() {
				t.Fatal("app-server was killed before finishing cleanup", client.err)
			}
		})
	}
}

func fixtureExecutable(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PENCODE_CODEX_PATH", path)
	t.Setenv("PENCODE_CODEX_FIXTURE", "1")
	// Race binaries otherwise sleep for a second after cleanup, exceeding the
	// server's shutdown deadline even though the fixture has already exited.
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	return path
}

func TestLeavingConversationReleasesServer(t *testing.T) {
	fixtureExecutable(t)
	for _, ref := range []string{"history/other", "new", "projectchange", "close"} {
		t.Run(ref, func(t *testing.T) {
			root := t.TempDir()
			a := &agent{ctx: t.Context(), context: peb.ActionContext{ProjectPath: root}, conversations: make(map[string]*conversation), workspaceOpen: true}
			t.Cleanup(a.closeClient)
			if err := a.resume("saved"); err != nil {
				t.Fatal(err)
			}
			previous, client := a.selected, a.client
			previous.draft = "keep draft"
			previous.busy, previous.turnID = true, "turn1"
			// Make the fixture own the same active turn without running inference.
			if _, err := call[json.RawMessage](client, "turn/start", struct {
				ThreadID string   `json:"threadId"`
				Input    [1]input `json:"input"`
			}{previous.ID, [1]input{{Type: "text", Text: "wait"}}}); err != nil {
				t.Fatal(err)
			}
			event := peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "click", Ref: ref}}
			if ref == "projectchange" || ref == "close" {
				event.Event.Kind = ref
				event.Context.ProjectPath = t.TempDir()
			}
			var err error
			if ref == "projectchange" {
				err = a.setContext(event.Context)
			} else {
				err = a.handleCommand(command{event: event})
			}
			if err != nil {
				t.Fatal(err)
			}
			a.exitConversations()
			if client.cmd.ProcessState == nil || !client.cmd.ProcessState.Success() {
				t.Fatal("previous app-server was not shut down before switching")
			}
			if previous.busy || previous.loaded || previous.turnID != "" || previous.items != nil || previous.requests != nil || previous.draft != "keep draft" || a.conversations[previous.ID] != previous {
				t.Fatal("leaving retained an active conversation or lost its draft/history")
			}
			if err := a.setContext(peb.ActionContext{ProjectPath: root}); err != nil {
				t.Fatal(err)
			}
			a.workspaceOpen = true
			if err := a.resume(previous.ID); err != nil {
				t.Fatal("released conversation could not be reopened", err)
			}
			if a.selected != previous || !previous.loaded || previous.draft != "keep draft" || a.client == client {
				t.Fatal("reopening did not restore the conversation on a new server")
			}
			current := a.client
			if err := a.resume(previous.ID); err != nil || a.client != current {
				t.Fatal("selecting the same conversation restarted its server", err)
			}
		})
	}
}

func TestSwitchingIdleConversationReleasesServer(t *testing.T) {
	fixtureExecutable(t)
	a := &agent{ctx: t.Context(), context: peb.ActionContext{ProjectPath: t.TempDir()}, conversations: make(map[string]*conversation)}
	t.Cleanup(a.closeClient)
	if err := a.resume("first"); err != nil {
		t.Fatal(err)
	}
	previous, client := a.selected, a.client
	if err := a.resume("second"); err != nil {
		t.Fatal(err)
	}
	if client.cmd.ProcessState == nil || !client.cmd.ProcessState.Success() || previous.loaded || a.selected.ID != "second" || !a.selected.loaded {
		t.Fatal("idle conversation retained its server or prevented the next resume")
	}
}

func TestCloseWorkspaceDuringThreadRequest(t *testing.T) {
	for _, method := range []string{"thread/start", "thread/resume"} {
		t.Run(method, func(t *testing.T) {
			var output bytes.Buffer
			a := &agent{workspaceOpen: true, commands: make(chan command, 1), conversations: make(map[string]*conversation)}
			client := &rpcClient{ctx: t.Context(), writer: json.NewEncoder(&output), messages: make(chan envelope, 1), commands: a.commands, handle: a.onMessage, done: make(chan struct{}), stdin: closeWriter{&output}}
			client.cancel = func() { close(client.done) }
			a.client = client
			client.dispatch = func(cmd command) {
				a.handlePendingCommand(cmd)
				client.messages <- envelope{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"thread":{"id":"thread"}}`)}
			}
			a.commands <- command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "close"}}}
			if method == "thread/start" {
				if _, err := a.startThread(); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := a.resume("thread"); err != nil {
					t.Fatal(err)
				}
			}
			if a.workspaceOpen || a.selected != nil || !a.exitsPending || !a.conversations["thread"].exitRequested {
				t.Fatal("pending thread request reopened the closed conversation")
			}
			a.exitConversations()
			if a.client != nil || a.conversations["thread"].loaded {
				t.Fatal("pending thread was not released")
			}
		})
	}
}

func TestAppServerShutdownHasDeadline(t *testing.T) {
	path := fixtureExecutable(t)
	t.Setenv("PENCODE_CODEX_SHUTDOWN_GATE", t.TempDir()+"/blocked")
	client, err := startClient(t.Context(), t.TempDir(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	client.close()
	if client.cmd.ProcessState.Success() || time.Since(started) > 3*time.Second {
		t.Fatal("unresponsive server was not reaped within the shutdown deadline")
	}
}

func TestCloseWorkspaceWhileSwitchingConversation(t *testing.T) {
	fixtureExecutable(t)
	var output bytes.Buffer
	ctx := peb.ActionContext{ProjectPath: t.TempDir()}
	previous := &conversation{thread: thread{ID: "previous"}, context: ctx, busy: true, turnID: "turn"}
	a := &agent{ctx: t.Context(), context: ctx, selected: previous, workspaceOpen: true, commands: make(chan command, 1), conversations: map[string]*conversation{previous.ID: previous}}
	t.Cleanup(a.closeClient)
	client := &rpcClient{ctx: t.Context(), writer: json.NewEncoder(&output), messages: make(chan envelope, 1), commands: a.commands, handle: a.onMessage, done: make(chan struct{}), stdin: closeWriter{&output}}
	client.cancel = func() { close(client.done) }
	a.client = client
	client.dispatch = func(cmd command) {
		a.handlePendingCommand(cmd)
		client.messages <- envelope{ID: json.RawMessage(`1`), Result: json.RawMessage(`{}`)}
	}
	a.commands <- command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "close"}}}
	if err := a.resume("next"); err != nil {
		t.Fatal(err)
	}
	if a.workspaceOpen || a.selected != nil || !a.exitsPending {
		t.Fatal("switching reopened the conversation after the tab closed")
	}
	current := a.client
	a.exitConversations()
	if a.client != nil || !current.cmd.ProcessState.Success() {
		t.Fatal("conversation remained occupied after closing during a switch")
	}
}
