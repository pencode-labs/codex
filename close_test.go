package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pencode-labs/pencode/peb"
)

func TestCloseWorkspaceDetachesAfterPendingTurnStart(t *testing.T) {
	var output bytes.Buffer
	c := &conversation{thread: thread{ID: "thread"}, busy: true, loaded: true, draft: "keep draft"}
	a := &agent{selected: c, workspaceOpen: true, commands: make(chan command, 1), conversations: map[string]*conversation{c.ID: c}}
	client := &rpcClient{ctx: t.Context(), writer: json.NewEncoder(&output), messages: make(chan envelope, 1), commands: a.commands, handle: a.onMessage, done: make(chan struct{})}
	client.cancel = func() { close(client.done) }
	a.client = client
	client.dispatch = func(cmd command) {
		a.handlePendingCommand(cmd)
		client.messages <- envelope{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"turn":{"id":"turn","status":"inProgress"}}`)}
	}
	a.commands <- command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "close"}}}
	_, err := call[json.RawMessage](client, "turn/start", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if a.workspaceOpen || a.selected != nil || !a.exitsPending {
		t.Fatal("pending request reopened the closed view")
	}
	a.exitConversations()
	var request envelope
	if err := json.Unmarshal(output.Bytes(), &request); err != nil || request.Method != "turn/start" {
		t.Fatal("closing sent an extra mutation or interrupt", output.String(), err)
	}
	if a.client != nil || c.loaded || c.busy || c.draft != "keep draft" || a.conversations[c.ID] != c {
		t.Fatal("detaching retained live UI state or lost the conversation draft")
	}
}

func TestLeavingConversationDisconnectsWithoutInterrupt(t *testing.T) {
	fixtureExecutable(t)
	for _, ref := range []string{"history/other", "projectchange", "close"} {
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
			select {
			case <-client.done:
			default:
				t.Fatal("previous subscriber remained connected")
			}
			if previous.busy || previous.loaded || previous.turnID != "" || previous.items != nil || previous.requests != nil || previous.draft != "keep draft" {
				t.Fatal("detaching retained live state or lost the draft")
			}
			if err := a.setContext(peb.ActionContext{ProjectPath: root}); err != nil {
				t.Fatal(err)
			}
			a.workspaceOpen = true
			if err := a.resume(previous.ID); err != nil {
				t.Fatal(err)
			}
			if a.selected != previous || !previous.loaded || previous.draft != "keep draft" || a.client == client {
				t.Fatal("reopening did not restore the conversation")
			}
			current := a.client
			if err := a.resume(previous.ID); err != nil || a.client != current {
				t.Fatal("reselecting restarted the connection", err)
			}
		})
	}
}

func TestCloseWorkspaceDuringThreadRequest(t *testing.T) {
	for _, method := range []string{"thread/start", "thread/resume"} {
		t.Run(method, func(t *testing.T) {
			var output bytes.Buffer
			a := &agent{workspaceOpen: true, commands: make(chan command, 1), conversations: make(map[string]*conversation)}
			client := &rpcClient{ctx: t.Context(), writer: json.NewEncoder(&output), messages: make(chan envelope, 1), commands: a.commands, handle: a.onMessage, done: make(chan struct{})}
			client.cancel = func() { close(client.done) }
			a.client = client
			client.dispatch = func(cmd command) {
				a.handlePendingCommand(cmd)
				client.messages <- envelope{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"thread":{"id":"thread"}}`)}
			}
			a.commands <- command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "close"}}}
			var err error
			if method == "thread/start" {
				_, err = a.startThread()
			} else {
				err = a.resume("thread")
			}
			if err != nil {
				t.Fatal(err)
			}
			if a.workspaceOpen || a.selected != nil || !a.exitsPending || !a.conversations["thread"].exitRequested {
				t.Fatal("pending request reopened the view")
			}
			a.exitConversations()
			if a.client != nil || a.conversations["thread"].loaded {
				t.Fatal("pending subscriber was not disconnected")
			}
		})
	}
}

func TestCloseRequestDetachesWithoutConfirmation(t *testing.T) {
	var published capturedUI
	c := &conversation{thread: thread{ID: "thread"}, busy: true, loaded: true}
	a := &agent{publisher: &published, selected: c, workspaceOpen: true, conversations: map[string]*conversation{c.ID: c}}
	a.handlePendingCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "closerequest"}}})
	if a.pendingCount != 0 || published.ui.ID != "composer" || !published.ui.Close || !c.busy || c.exitRequested {
		t.Fatal("closing prompted or stopped a running turn", published.ui)
	}
}

func TestClosingConnectionDoesNotWaitForPeer(t *testing.T) {
	path := fixtureExecutable(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	client, err := startClient(ctx, t.TempDir(), path, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = call[json.RawMessage](client, "initialize", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	cancel()
	client.close()
	if time.Since(started) > time.Second {
		t.Fatal("disconnect waited for server shutdown")
	}
}
