package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pencode-labs/pencode/peb"
)

func TestTurnHistoryCompletionAndExpansion(t *testing.T) {
	c := &conversation{thread: thread{ID: "thread"}, draft: "keep draft"}
	a := &agent{selected: c, conversations: map[string]*conversation{c.ID: c}}
	notify := func(method, params string) {
		t.Helper()
		a.onMessage(envelope{Method: method, Params: json.RawMessage(params)})
	}
	notify("turn/started", `{"threadId":"thread","turn":{"id":"one","status":"inProgress"}}`)
	for _, item := range []string{
		`{"id":"user","type":"userMessage","content":[{"type":"text","text":"用户任务"}]}`,
		`{"id":"commentary","type":"agentMessage","phase":"commentary","text":"中间进展"}`,
		`{"id":"cmd","type":"commandExecution","command":"go test ./...","status":"completed","aggregatedOutput":"tests passed"}`,
		`{"id":"edit","type":"fileChange","status":"completed","changes":[{"path":"ui.go","diff":"+updated\n"}]}`,
		`{"id":"final","type":"agentMessage","phase":"final_answer","text":"最终回复"}`,
	} {
		notify("item/completed", `{"threadId":"thread","item":`+item+`}`)
	}
	view := a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, "中间进展") || strings.Contains(view.HTML, `class="codex-turn-toggle"`) {
		t.Fatal("running history must stay visible", view.HTML)
	}
	clickActivity(t, a, "activity/cmd")
	clickActivity(t, a, "item/cmd")
	notify("turn/completed", `{"threadId":"thread","turn":{"id":"one","status":"completed","durationMs":85000}}`)
	view = a.composerView()
	validateBindings(t, view)
	for _, want := range []string{"用时 1m 25s", "用户任务", "最终回复", "ui.go", `data-ref="turn/one" aria-expanded="false"`} {
		if !strings.Contains(view.HTML, want) {
			t.Fatalf("completed turn lost %q: %s", want, view.HTML)
		}
	}
	if strings.Contains(view.HTML, "中间进展") || strings.Contains(view.HTML, "tests passed") || strings.Contains(view.HTML, `data-ref="item/cmd"`) {
		t.Fatal("completed history and its controls must start folded", view.HTML)
	}
	template, _, err := peb.SplitViewRegions(view.HTML)
	if err != nil {
		t.Fatal(err)
	}
	for _, expanded := range []bool{true, false, true} {
		clickActivity(t, a, "turn/one")
		view = a.composerView()
		validateBindings(t, view)
		if c.progress.expanded != expanded || strings.Contains(view.HTML, "中间进展") != expanded || strings.Contains(view.HTML, "tests passed") != expanded {
			t.Fatal("toggle lost history or nested expansion state", view.HTML)
		}
		updated, _, err := peb.SplitViewRegions(view.HTML)
		if err != nil || updated != template || !strings.Contains(view.HTML, "最终回复") || c.draft != "keep draft" {
			t.Fatal("history toggle changed the surrounding transcript or draft", err)
		}
	}
	previous := c.progress
	notify("turn/started", `{"threadId":"thread","turn":{"id":"two","status":"inProgress"}}`)
	notify("item/completed", `{"threadId":"thread","item":{"id":"user2","type":"userMessage","content":[{"type":"text","text":"下一轮任务"}]}}`)
	clickActivity(t, a, "turn/one")
	if previous.expanded || c.progress == previous || c.progress.complete || !strings.Contains(a.composerView().HTML, "正在思考") {
		t.Fatal("toggling an earlier turn affected the current turn")
	}
	if allocations := testing.AllocsPerRun(100, func() {
		_ = a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "click", Ref: "turn/one"}}})
	}); allocations != 0 {
		t.Fatalf("warm history toggles allocated %g objects", allocations)
	}
}

func TestTurnHistoryRestoresAndRetainsExpansion(t *testing.T) {
	for _, status := range []string{"completed", "interrupted", "failed"} {
		t.Run(status, func(t *testing.T) {
			older := turn{ID: "older", Status: status, StartedAt: 100, CompletedAt: 185, Items: []threadItem{
				{ID: "user", Type: "userMessage", Text: "用户任务"},
				{ID: "commentary", Type: "agentMessage", Phase: "commentary", Text: "中间进展"},
			}}
			if status == "completed" {
				older.Items = append(older.Items, threadItem{ID: "final", Type: "agentMessage", Phase: "final_answer", Text: "最终回复"})
			}
			client, _ := snapshotClient(t, turnPage{Data: []turn{older}})
			c := &conversation{thread: thread{ID: "thread"}}
			a := &agent{selected: c, client: client}
			if err := a.readHistory(c); err != nil {
				t.Fatal(err)
			}
			view := a.composerView()
			validateBindings(t, view)
			if !strings.Contains(view.HTML, "用时 1m 25s") || strings.Contains(view.HTML, "中间进展") {
				t.Fatal("restored history should start collapsed", view.HTML)
			}
			clickActivity(t, a, "turn/older")
			progress := c.turnProgress(older.ID)
			newer := turn{ID: "newer", Status: "completed", StartedAt: 200, CompletedAt: 210, Items: []threadItem{
				{ID: "new-user", Type: "userMessage", Text: "新任务"},
				{ID: "new-final", Type: "agentMessage", Text: "新回复"},
			}}
			a.reconcileObserved(c, []turn{newer, older})
			a.reconcileObserved(c, []turn{newer, older})
			view = a.composerView()
			validateBindings(t, view)
			if c.turnProgress(older.ID) != progress || !progress.expanded || !strings.Contains(view.HTML, "中间进展") {
				t.Fatal("snapshot replacement lost an earlier turn's expansion", view.HTML)
			}
			if strings.Count(view.HTML, `class="codex-turn-toggle"`) != 1 || strings.Count(view.HTML, "用时 ") != 2 {
				t.Fatal("a turn without intermediate history should show duration alone", view.HTML)
			}
		})
	}
}

func TestCompletedTurnDurationFormat(t *testing.T) {
	for _, tc := range []struct {
		elapsed time.Duration
		want    string
	}{
		{0, "用时 0s"},
		{59 * time.Second, "用时 59s"},
		{time.Minute, "用时 1m 0s"},
		{time.Hour + 2*time.Minute + 3*time.Second, "用时 1h 2m 3s"},
	} {
		var b strings.Builder
		p := turnProgress{complete: true, elapsed: tc.elapsed}
		p.render(&b, false)
		if !strings.Contains(b.String(), tc.want) || strings.Contains(b.String(), "已处理") {
			t.Fatal("incorrect completed duration", b.String())
		}
	}
}
