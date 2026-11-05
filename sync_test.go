package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pencode-labs/pencode/peb"
)

type syncPublisher struct{ updates chan peb.UI }

func (p syncPublisher) Show(ui peb.UI) error {
	select {
	case p.updates <- ui:
	default:
	}
	return nil
}
func (p syncPublisher) CloseUI(id string) error { return nil }

func TestTranscriptClicksRemainResponsiveDuringObserverRPC(t *testing.T) {
	var output bytes.Buffer
	var published capturedUI
	i := newChatItem(threadItem{ID: "command", Type: "commandExecution", Command: "go test", Status: "inProgress", AggregatedOutput: "live output"}, false)
	c := &conversation{thread: thread{ID: "external"}, observing: true, busy: true, items: []*chatItem{i}}
	a := &agent{publisher: &published, selected: c, workspaceOpen: true, workspaceDirty: true, commands: make(chan command, 1)}
	a.flush()
	client := &rpcClient{ctx: t.Context(), writer: json.NewEncoder(&output), messages: make(chan envelope, 1), commands: a.commands}
	client.dispatch = func(cmd command) {
		a.handlePendingCommand(cmd)
		if a.pendingCount != 0 || published.ui.View == nil || !strings.Contains(published.ui.View.HTML, `data-ref="item/command"`) {
			t.Error("activity click waited for the observer response")
		}
		if cmd.event.Event.Ref == "item/command" && !strings.Contains(published.ui.View.HTML, "live output") {
			t.Error("command expansion waited for the observer response")
		}
		// The view must already be published before the server responds.
		client.messages <- envelope{ID: json.RawMessage(fmt.Sprint(client.next)), Result: json.RawMessage(`{}`)}
	}
	for _, ref := range []string{"activity/command", "item/command"} {
		a.commands <- command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "click", Ref: ref}}}
		if _, err := call[json.RawMessage](client, "thread/turns/list", struct{}{}); err != nil {
			t.Fatal(err)
		}
	}
	if !i.groupExpanded || !i.expanded {
		t.Fatal("running transcript did not expand")
	}
	if strings.Count(output.String(), `"method"`) != 2 {
		t.Fatal("transcript clicks started a nested RPC", output.String())
	}
}

func TestExternalObserverAutomaticallyPollsWhileHidden(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var completed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var request envelope
			if err = json.Unmarshal(raw, &request); err != nil {
				return
			}
			response := envelope{ID: request.ID}
			var result any = struct{}{}
			switch request.Method {
			case "initialized":
				continue
			case "thread/list":
				result = threadPage{Data: []thread{{ID: "external"}}}
			case "thread/resume":
				response.Error = &rpcError{Code: -32600, Message: "thread external already has an active writer"}
			case "thread/read":
				result = struct {
					Thread thread `json:"thread"`
				}{thread{ID: "external"}}
			case "thread/turns/list":
				latest := turn{ID: "turn", Status: "interrupted", StartedAt: time.Now().Unix(), Items: []threadItem{{ID: "reply", Type: "agentMessage", Text: "running reply"}}}
				if completed.Load() {
					latest.Status, latest.CompletedAt, latest.Items[0].Text = "completed", time.Now().Unix(), "finished reply"
				}
				result = turnPage{Data: []turn{latest}}
			}
			response.Result, _ = json.Marshal(result)
			raw, _ = json.Marshal(response)
			if conn.Write(ctx, websocket.MessageText, raw) != nil {
				return
			}
		}
	}))
	defer server.Close()
	updates := make(chan peb.UI, 32)
	a := &agent{ctx: ctx, publisher: syncPublisher{updates}, serverURL: "ws" + strings.TrimPrefix(server.URL, "http"), commands: make(chan command, 8), done: make(chan struct{})}
	go a.run()
	defer func() { cancel(); <-a.done }()
	root := t.TempDir()
	if err := a.submit(command{action: true, context: peb.ActionContext{ProjectPath: root}}); err != nil {
		t.Fatal(err)
	}
	if err := a.submit(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "click", Ref: "history/external"}}}); err != nil {
		t.Fatal(err)
	}
	waitUI := func(predicate func(peb.UI) bool) {
		t.Helper()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case ui := <-updates:
				if predicate(ui) {
					return
				}
			case <-deadline.C:
				t.Fatal("automatic observer update did not arrive")
			}
		}
	}
	waitUI(func(ui peb.UI) bool { return ui.Busy })
	if err := a.submit(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "visibilitychange", Value: "hidden"}}}); err != nil {
		t.Fatal(err)
	}
	completed.Store(true)
	waitUI(func(ui peb.UI) bool {
		return !ui.Busy && ui.Unread && ui.View != nil && strings.Contains(ui.View.HTML, "finished reply")
	})
	if err := a.submit(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "visibilitychange", Value: "visible"}}}); err != nil {
		t.Fatal(err)
	}
	waitUI(func(ui peb.UI) bool {
		return !ui.Busy && !ui.Unread && ui.View != nil && strings.Contains(ui.View.HTML, "finished reply")
	})
}

func snapshotClient(t *testing.T, pages ...turnPage) (*rpcClient, *bytes.Buffer) {
	t.Helper()
	var output bytes.Buffer
	c := &rpcClient{ctx: t.Context(), writer: json.NewEncoder(&output), messages: make(chan envelope, len(pages))}
	for index, page := range pages {
		raw, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		c.messages <- envelope{ID: json.RawMessage(fmt.Sprint(index + 1)), Result: raw}
	}
	return c, &output
}

func TestExternalSnapshotsUpdateContentAndPreserveView(t *testing.T) {
	active := turn{ID: "active", Status: "interrupted", StartedAt: 100, Items: []threadItem{
		{ID: "reply", Type: "agentMessage", Text: "First reply"},
		{ID: "command", Type: "commandExecution", Command: "go test", Status: "inProgress", AggregatedOutput: "first output"},
		{ID: "edit", Type: "fileChange", Status: "completed", Changes: []change{{Path: "main.go", Diff: "+one\n"}}},
	}}
	updated := active
	updated.Items = append([]threadItem(nil), active.Items...)
	updated.Items[0].Text = "First reply, continued"
	updated.Items[1].AggregatedOutput = "first output\nsecond output"
	finished := updated
	finished.Status, finished.CompletedAt = "completed", 110
	finished.Items = append([]threadItem(nil), updated.Items...)
	finished.Items[1].Status = "completed"
	client, output := snapshotClient(t, turnPage{Data: []turn{active}}, turnPage{Data: []turn{updated}}, turnPage{Data: []turn{finished}}, turnPage{Data: []turn{finished}})
	older := newChatItem(threadItem{ID: "older", Type: "agentMessage", Text: "older history"}, true)
	older.files = newTurnFiles("older-turn", "")
	c := &conversation{thread: thread{ID: "external"}, observing: true, draft: "keep draft", cursor: "older-cursor", items: []*chatItem{older}, status: externalSessionStatus}
	a := &agent{client: client, conversations: map[string]*conversation{c.ID: c}, selected: c, workspaceOpen: true, showHistory: true, history: threadPage{Data: []thread{c.thread}}}
	if err := a.syncConversation(c); err != nil {
		t.Fatal(err)
	}
	if !c.busy || c.turnID != active.ID || c.loaded || c.unread || c.progress == nil {
		t.Fatal("external work was not restored", c)
	}
	view := a.composerView()
	validateBindings(t, view)
	if strings.Contains(view.HTML, `data-ref="stop"`) || !strings.Contains(view.HTML, "请在原客户端停止") || !strings.Contains(view.HTML, "codex-history-running") {
		t.Fatal("observer exposed controls or lost activity", view.HTML)
	}
	item := c.items[1]
	item.expanded, item.groupExpanded = true, true
	c.files.collapsed, c.files.files[0].expanded = false, true
	if err := a.syncConversation(c); err != nil {
		t.Fatal(err)
	}
	if c.items[0] != older || c.items[1] != item || !item.expanded || !item.groupExpanded || item.text.String() != updated.Items[0].Text || c.items[2].output.String() != updated.Items[1].AggregatedOutput || c.files.added != 1 || c.files.collapsed || !c.files.files[0].expanded {
		t.Fatal("snapshot lost updates or view state")
	}
	if c.draft != "keep draft" || c.cursor != "older-cursor" || len(c.items) != 4 {
		t.Fatal("snapshot damaged draft, history or pagination")
	}
	a.workspaceHidden = true
	if err := a.syncConversation(c); err != nil {
		t.Fatal(err)
	}
	if c.busy || !c.unread || !c.progress.complete || c.progress.elapsed.Seconds() != 10 || !c.files.complete || !strings.Contains(a.historyView().HTML, "codex-history-unread") {
		t.Fatal("background completion did not produce unread result", c)
	}
	files := c.files
	a.markViewed(c)
	c.dirty = false
	if err := a.syncConversation(c); err != nil {
		t.Fatal(err)
	}
	if c.dirty || c.unread || c.files != files {
		t.Fatal("unchanged snapshot rebuilt UI or restored unread")
	}
	if strings.Contains(output.String(), `thread/resume`) || strings.Contains(output.String(), `turn/interrupt`) {
		t.Fatal("observer attempted to control the writer")
	}
}

func TestExternalSnapshotsCatchUpAcrossPages(t *testing.T) {
	makeTurn := func(id string) turn {
		return turn{ID: id, Status: "completed", Items: []threadItem{{ID: id, Type: "agentMessage", Text: id}}}
	}
	client, output := snapshotClient(t,
		turnPage{Data: []turn{makeTurn("third")}, NextCursor: "opaque-next"},
		turnPage{Data: []turn{makeTurn("second"), makeTurn("first")}},
	)
	first := newChatItem(makeTurn("first").Items[0], true)
	first.files = newTurnFiles("first", "")
	c := &conversation{thread: thread{ID: "external"}, observing: true, observedTurn: "first", observedStatus: "completed", items: []*chatItem{first}}
	a := &agent{client: client}
	if err := a.syncConversation(c); err != nil {
		t.Fatal(err)
	}
	if len(c.items) != 3 || c.items[0] != first || c.items[1].text.String() != "second" || c.items[2].text.String() != "third" || !c.unread {
		t.Fatal("catchup skipped or reordered turns", c.items)
	}
	if !strings.Contains(output.String(), `"cursor":"opaque-next"`) {
		t.Fatal("catchup did not use server cursor", output.String())
	}
}

func TestExternalInterruptionDoesNotStayBusyOrBecomeUnread(t *testing.T) {
	for _, status := range []string{"interrupted", "failed"} {
		t.Run(status, func(t *testing.T) {
			c := &conversation{observing: true, busy: true, observedTurn: "turn", observedStatus: "inProgress"}
			a := &agent{}
			latest := turn{ID: "turn", Status: status, StartedAt: 100, CompletedAt: 105}
			normalizeObservedTurn(&latest)
			a.reconcileObserved(c, []turn{latest})
			if c.busy || c.unread || c.observedStatus != status {
				t.Fatal("terminal status was inferred as running", c)
			}
		})
	}
}

func TestThreadRuntimeStatusUpdatesKnownAndUnopenedSessions(t *testing.T) {
	c := &conversation{thread: thread{ID: "known"}, loaded: true}
	a := &agent{conversations: map[string]*conversation{c.ID: c}, showHistory: true, history: threadPage{Data: []thread{c.thread, {ID: "unopened"}}}}
	for _, id := range []string{"known", "unopened"} {
		a.onMessage(envelope{Method: "thread/status/changed", Params: json.RawMessage(fmt.Sprintf(`{"threadId":%q,"status":{"type":"active","activeFlags":["waitingOnApproval"]}}`, id))})
	}
	if !c.busy || c.unread || a.history.Data[1].Status.Type != "active" {
		t.Fatal("runtime status was ignored")
	}
	if strings.Count(a.historyView().HTML, `class="codex-history-running"`) != 2 {
		t.Fatal("sidebar omitted running thread")
	}
	a.updateThreadStatus(c.ID, threadStatus{Type: "idle"})
	if c.busy || c.unread {
		t.Fatal("idle status left stale running state")
	}
	if n := testing.AllocsPerRun(100, func() { a.updateThreadStatus(c.ID, threadStatus{Type: "active"}) }); n != 0 {
		t.Fatalf("runtime update allocates: %g", n)
	}
}

func TestExternalObserverReopenKeepsDraftAndExpansion(t *testing.T) {
	client, output := snapshotClient(t, turnPage{Data: []turn{{ID: "turn", Status: "completed", Items: []threadItem{{ID: "reply", Type: "agentMessage", Text: "reply"}}}}})
	item := newChatItem(threadItem{ID: "reply", Type: "agentMessage", Text: "old"}, true)
	item.files, item.expanded = newTurnFiles("turn", ""), true
	c := &conversation{thread: thread{ID: "external"}, context: peb.ActionContext{ProjectPath: t.TempDir()}, observing: true, unread: true, draft: "draft", observedTurn: "turn", observedStatus: "completed", items: []*chatItem{item}}
	a := &agent{client: client, conversations: map[string]*conversation{c.ID: c}, context: c.context}
	if err := a.openConversation(c.ID); err != nil {
		t.Fatal(err)
	}
	if a.selected != c || c.items[0] != item || !item.expanded || c.draft != "draft" || c.unread || strings.Contains(output.String(), `thread/resume`) {
		t.Fatal("reopen lost observer state")
	}
}
