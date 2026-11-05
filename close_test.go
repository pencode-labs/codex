package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pencode-labs/pencode/peb"
)

func TestCloseWorkspaceKeepsPendingTurnStart(t *testing.T) {
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
	if _, err := call[json.RawMessage](client, "turn/start", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if a.workspaceOpen || a.selected != c || a.client != client || !c.busy || !c.loaded || c.draft != "keep draft" {
		t.Fatal("closing lost a running conversation")
	}
	var request envelope
	if err := json.Unmarshal(output.Bytes(), &request); err != nil || request.Method != "turn/start" {
		t.Fatal("closing sent an extra mutation", output.String(), err)
	}
}

func TestSwitchingAndClosingRetainSubscriptions(t *testing.T) {
	fixtureExecutable(t)
	for _, ref := range []string{"new", "history/other", "projectchange", "close"} {
		t.Run(ref, func(t *testing.T) {
			root := t.TempDir()
			a := &agent{publisher: &capturedUI{}, ctx: t.Context(), context: peb.ActionContext{ProjectPath: root}, conversations: make(map[string]*conversation), workspaceOpen: true}
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
			if err := a.handleCommand(command{event: event}); err != nil {
				t.Fatal(err)
			}
			if a.client != client || !previous.busy || !previous.loaded || previous.turnID != "turn1" || previous.draft != "keep draft" {
				t.Fatal("leaving conversation lost live state")
			}
			if err := a.setContext(peb.ActionContext{ProjectPath: root}); err != nil {
				t.Fatal(err)
			}
			a.workspaceOpen = true
			if err := a.resume(previous.ID); err != nil || a.selected != previous || a.client != client {
				t.Fatal("reopening replaced the live connection", err)
			}
		})
	}
}

func TestCloseRequestContinuesWithoutConfirmation(t *testing.T) {
	var published capturedUI
	c := &conversation{thread: thread{ID: "thread"}, busy: true, loaded: true}
	a := &agent{publisher: &published, selected: c, workspaceOpen: true, conversations: map[string]*conversation{c.ID: c}}
	a.handlePendingCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "closerequest"}}})
	if a.pendingCount != 0 || published.ui.ID != "composer" || !published.ui.Close || !c.busy {
		t.Fatal("closing prompted or stopped a running turn", published.ui)
	}
}

func TestLifecycleCountsHiddenConversationsByProject(t *testing.T) {
	a := &agent{conversations: map[string]*conversation{
		"one":  {context: peb.ActionContext{ProjectPath: "/one"}, busy: true},
		"two":  {context: peb.ActionContext{ProjectPath: "/two"}, busy: true},
		"idle": {context: peb.ActionContext{ProjectPath: "/one"}},
	}}
	for _, tc := range []struct {
		count string
		event peb.LifecycleEvent
	}{
		{event: peb.LifecycleEvent{Kind: peb.WindowClosing}, count: "2"},
		{event: peb.LifecycleEvent{Kind: peb.ProjectClosing, ProjectPath: "/one"}, count: "1"},
		{event: peb.LifecycleEvent{Kind: peb.ProjectClosing, ProjectPath: "/idle"}, count: ""},
	} {
		reply := a.handleLifecycle(tc.event)
		if tc.count == "" && reply.Message != "" || tc.count != "" && !strings.Contains(reply.Message, tc.count+" 个会话") {
			t.Fatal("wrong running count", reply)
		}
	}
	a.handleLifecycle(peb.LifecycleEvent{Kind: peb.ProjectClosed, ProjectPath: "/one"})
	if len(a.conversations) != 1 || !a.conversations["two"].busy {
		t.Fatal("project closure affected another project")
	}
}

func TestBackgroundCompletionUnreadUntilViewed(t *testing.T) {
	for _, mode := range []string{"foreground", "other conversation", "hidden tab", "closed tab"} {
		t.Run(mode, func(t *testing.T) {
			c := &conversation{thread: thread{ID: "thread"}, busy: true}
			a := &agent{selected: c, workspaceOpen: true, conversations: map[string]*conversation{c.ID: c}, history: threadPage{Data: []thread{c.thread}}, showHistory: true}
			switch mode {
			case "other conversation":
				a.selected = nil
			case "hidden tab":
				a.workspaceHidden = true
			case "closed tab":
				a.workspaceOpen = false
			}
			if !strings.Contains(a.historyView().HTML, `class="codex-history-running"`) {
				t.Fatal("missing running indicator")
			}
			a.onMessage(envelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"status":"completed"}}`)})
			wantUnread := mode != "foreground"
			if c.busy || c.unread != wantUnread || !a.historyDirty {
				t.Fatal("incorrect completion state", c)
			}
			view := a.historyView().HTML
			if strings.Contains(view, `class="codex-history-running"`) || strings.Contains(view, `class="codex-history-unread"`) != wantUnread {
				t.Fatal("completion indicator incorrect", view)
			}
			a.selectConversation(c)
			if c.unread || strings.Contains(a.historyView().HTML, `class="codex-history-unread"`) {
				t.Fatal("viewing did not clear unread")
			}
		})
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

func TestHiddenTabUnreadMetadataClearsOnVisibility(t *testing.T) {
	var published capturedUI
	c := &conversation{thread: thread{ID: "thread"}, busy: true}
	a := &agent{publisher: &published, selected: c, workspaceOpen: true, workspaceHidden: true, conversations: map[string]*conversation{c.ID: c}}
	a.onMessage(envelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"status":"completed"}}`)})
	a.flush()
	if !published.ui.Unread || published.ui.Busy {
		t.Fatal("background completion did not mark the editor tab")
	}
	if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "visibilitychange", Value: "visible"}}}); err != nil {
		t.Fatal(err)
	}
	a.flush()
	if c.unread || published.ui.Unread {
		t.Fatal("viewing editor tab retained unread marker")
	}
	if n := testing.AllocsPerRun(100, func() { c.unread = true; a.markViewed(c) }); n != 0 {
		t.Fatalf("marking a conversation viewed allocated %g times", n)
	}
}

func TestLifecycleRequestRemainsResponsiveDuringTurnRPC(t *testing.T) {
	var output bytes.Buffer
	c := &conversation{thread: thread{ID: "thread"}, busy: true}
	event := peb.LifecycleEvent{Kind: peb.WindowClosing}
	reply := make(chan peb.LifecycleReply, 1)
	a := &agent{conversations: map[string]*conversation{c.ID: c}, commands: make(chan command, 1)}
	client := &rpcClient{ctx: t.Context(), writer: json.NewEncoder(&output), messages: make(chan envelope, 1), commands: a.commands, done: make(chan struct{})}
	client.dispatch = func(cmd command) {
		a.handlePendingCommand(cmd)
		client.messages <- envelope{ID: json.RawMessage(`1`), Result: json.RawMessage(`{}`)}
	}
	a.commands <- command{lifecycle: &event, reply: reply}
	if _, err := call[json.RawMessage](client, "turn/start", struct{}{}); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-reply:
		if !strings.Contains(result.Message, "1 个会话") {
			t.Fatal(result)
		}
	default:
		t.Fatal("close check was deferred until the pending RPC completed")
	}
}
