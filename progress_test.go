package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pencode-labs/pencode/peb"
)

func TestTurnProgressLifecycle(t *testing.T) {
	for _, method := range []string{"item/agentMessage/delta", "item/plan/delta", "item/reasoning/summaryTextDelta", "item/reasoning/textDelta"} {
		t.Run(method, func(t *testing.T) {
			c := &conversation{thread: thread{ID: "thread"}}
			a := &agent{selected: c, workspaceOpen: true, conversations: map[string]*conversation{c.ID: c}}
			notify := func(method, params string) {
				t.Helper()
				a.onMessage(envelope{Method: method, Params: json.RawMessage(params)})
			}
			notify("turn/started", `{"threadId":"thread","turn":{"id":"one","status":"inProgress"}}`)
			notify("item/started", `{"threadId":"thread","item":{"id":"user","type":"userMessage","content":[{"type":"text","text":"任务"}]}}`)
			notify("item/started", `{"threadId":"thread","item":{"id":"reply","type":"agentMessage"}}`)
			view := a.composerView()
			if !strings.Contains(view.HTML, "正在思考") || strings.Contains(view.HTML, "已处理") || a.progressVisible() {
				t.Fatal("empty item must keep thinking without starting the update timer", view.HTML)
			}
			notify(method, `{"threadId":"thread","itemId":"reply","delta":""}`)
			if c.progress.received {
				t.Fatal("empty delta was counted as the first token")
			}
			c.progress.started = time.Now().Add(-112 * time.Second)
			notify(method, `{"threadId":"thread","itemId":"reply","delta":"回复"}`)
			view = a.composerView()
			validateBindings(t, view)
			if strings.Contains(view.HTML, "正在思考") || strings.Count(view.HTML, "已处理 1分钟 52秒") != 1 || !a.progressVisible() {
				t.Fatal("first token did not replace thinking with elapsed time", view.HTML)
			}
			if strings.Index(view.HTML, "任务") > strings.Index(view.HTML, `class="codex-turn-progress"`) || strings.Index(view.HTML, `class="codex-turn-progress"`) > strings.Index(view.HTML, `aria-label="Codex"`) {
				t.Fatal("progress must follow the user message and precede the reply", view.HTML)
			}
			notify("turn/completed", `{"threadId":"thread","turn":{"id":"one","status":"completed","durationMs":125000}}`)
			if !c.progress.complete || c.progress.elapsed != 125*time.Second || a.progressVisible() {
				t.Fatal("completion did not use the server duration", c.progress)
			}
			a.updateProgress(time.Now().Add(time.Hour))
			if !strings.Contains(a.composerView().HTML, "已处理 2分钟 5秒") {
				t.Fatal("completed elapsed time changed")
			}
			first := c.progress
			notify("turn/started", `{"threadId":"thread","turn":{"id":"two","status":"inProgress"}}`)
			view = a.composerView()
			if c.progress == first || strings.Count(view.HTML, `class="codex-turn-progress"`) != 2 || !strings.Contains(view.HTML, "正在思考") || !strings.Contains(view.HTML, "已处理 2分钟 5秒") {
				t.Fatal("new turn must preserve the previous turn's duration", view.HTML)
			}
		})
	}
}

func TestTurnProgressTimingSources(t *testing.T) {
	now := time.Unix(1000, 0)
	zero, server := int64(0), int64(112000)
	for _, tc := range []struct {
		name string
		turn turn
		want time.Duration
	}{
		{"server", turn{Status: "completed", DurationMs: &server, StartedAt: 100, CompletedAt: 200}, 112 * time.Second},
		{"zero", turn{Status: "completed", DurationMs: &zero}, 0},
		{"timestamps", turn{Status: "completed", StartedAt: 100, CompletedAt: 212}, 112 * time.Second},
		{"local", turn{Status: "completed"}, 10 * time.Second},
		{"interrupted", turn{Status: "interrupted"}, 10 * time.Second},
		{"failed", turn{Status: "failed"}, 10 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &turnProgress{started: now.Add(-10 * time.Second)}
			p.update(tc.turn, now)
			if !p.complete || p.elapsed != tc.want {
				t.Fatal("incorrect final duration", p)
			}
		})
	}
}

func TestTurnProgressPreservesEmptyFailedTurn(t *testing.T) {
	c := &conversation{thread: thread{ID: "thread"}}
	a := &agent{selected: c, workspaceOpen: true, conversations: map[string]*conversation{c.ID: c}}
	for _, event := range []struct{ method, params string }{
		{"turn/started", `{"threadId":"thread","turn":{"id":"one","status":"inProgress"}}`},
		{"item/completed", `{"threadId":"thread","item":{"id":"user1","type":"userMessage","content":[{"type":"text","text":"first"}]}}`},
		{"turn/completed", `{"threadId":"thread","turn":{"id":"one","status":"failed","durationMs":5000}}`},
		{"turn/started", `{"threadId":"thread","turn":{"id":"two","status":"inProgress"}}`},
		{"item/completed", `{"threadId":"thread","item":{"id":"user2","type":"userMessage","content":[{"type":"text","text":"second"}]}}`},
	} {
		a.onMessage(envelope{Method: event.method, Params: json.RawMessage(event.params)})
	}
	view := a.composerView()
	if strings.Count(view.HTML, `class="codex-turn-progress"`) != 2 || !strings.Contains(view.HTML, "已处理 5秒") || !strings.Contains(view.HTML, "正在思考") {
		t.Fatal("a turn without model output lost its duration", view.HTML)
	}
}

func TestTurnProgressUpdatesWithoutAllocations(t *testing.T) {
	now := time.Now()
	c := &conversation{busy: true, progress: &turnProgress{started: now.Add(-time.Minute), received: true}}
	a := &agent{selected: c, workspaceOpen: true}
	if allocations := testing.AllocsPerRun(100, func() {
		c.progress.elapsed, c.dirty = 0, false
		a.updateProgress(now)
	}); allocations != 0 {
		t.Fatalf("warm timing updates allocated %g objects", allocations)
	}
	if !c.dirty || c.progress.elapsed != time.Minute {
		t.Fatal("elapsed seconds did not schedule a view update")
	}
	c.dirty = false
	a.updateProgress(now.Add(time.Millisecond))
	if c.dirty {
		t.Fatal("same second triggered another view update")
	}
	a.workspaceOpen = false
	a.updateProgress(now.Add(time.Hour))
	if c.dirty || a.progressVisible() {
		t.Fatal("hidden workspace kept scheduling timing updates")
	}
}

func TestHistoryRestoresTurnDuration(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PENCODE_CODEX_PATH", path)
	t.Setenv("PENCODE_CODEX_FIXTURE", "1")
	t.Setenv("PENCODE_CODEX_TURN_DURATION", "1")
	a := &agent{ctx: t.Context(), context: peb.ActionContext{ProjectPath: t.TempDir()}, conversations: make(map[string]*conversation)}
	if err := a.resume("saved"); err != nil {
		t.Fatal(err)
	}
	defer a.client.close()
	view := a.composerView()
	if strings.Count(view.HTML, "已处理 1分钟 52秒") != 1 || strings.Contains(view.HTML, "正在思考") || !strings.Contains(view.HTML, "Stored conversation") {
		t.Fatal("history lost the server's turn duration", view.HTML)
	}
	t.Setenv("PENCODE_CODEX_TURN_DURATION", "started-only")
	a.client.close()
	a.client = nil
	a.selected.loaded = false
	if err := a.resume("saved"); err != nil {
		t.Fatal(err)
	}
	defer a.client.close()
	if strings.Contains(a.composerView().HTML, "已处理") {
		t.Fatal("history without a duration or end timestamp must not estimate elapsed time")
	}
}
