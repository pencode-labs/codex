package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pencode-labs/pencode/peb"
)

func TestMain(m *testing.M) {
	if os.Getenv("PENCODE_CODEX_FIXTURE") == "1" && len(os.Args) > 1 && os.Args[1] == "app-server" {
		fixtureServer()
		return
	}
	os.Exit(m.Run())
}

// A separate process exercises real JSONL pipes, including requests in the
// reverse direction. It never connects to OpenAI or touches workspace files.
func fixtureServer() {
	out := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if out.Encode(v) != nil {
			os.Exit(2)
		}
	}
	notify := func(method string, p any) { send(map[string]any{"method": method, "params": p}) }
	initialized := false
	count := 0
	modelPages := 0
	active := ""
	reader := bufio.NewScanner(os.Stdin)
	for reader.Scan() {
		var msg envelope
		if json.Unmarshal(reader.Bytes(), &msg) != nil {
			os.Exit(2)
		}
		if msg.Method == "initialized" {
			initialized = true
			continue
		}
		if msg.Method == "" {
			var decision struct {
				Decision string `json:"decision"`
			}
			_ = json.Unmarshal(msg.Result, &decision)
			notify("item/completed", map[string]any{"threadId": active, "item": map[string]any{"id": "cmd", "type": "commandExecution", "command": "go test ./...", "status": "completed", "aggregatedOutput": "tests passed"}})
			notify("item/agentMessage/delta", map[string]string{"threadId": active, "itemId": "reply", "delta": "Streamed "})
			notify("item/agentMessage/delta", map[string]string{"threadId": active, "itemId": "reply", "delta": "reply"})
			notify("item/completed", map[string]any{"threadId": active, "item": map[string]string{"id": "reply", "type": "agentMessage", "text": "**Final reply** " + decision.Decision}})
			notify("turn/completed", map[string]any{"threadId": active, "turn": map[string]string{"id": "turn1", "status": "completed"}})
			continue
		}
		response := func(v any) { send(map[string]any{"id": msg.ID, "result": v}) }
		if msg.Method != "initialize" && !initialized {
			os.Exit(3)
		}
		if msg.Method == "thread/start" || msg.Method == "thread/resume" {
			var params struct {
				Sandbox        string `json:"sandbox"`
				ApprovalPolicy string `json:"approvalPolicy"`
			}
			if json.Unmarshal(msg.Params, &params) != nil || params.Sandbox != "workspace-write" || params.ApprovalPolicy != "on-request" {
				os.Exit(4)
			}
		}
		switch msg.Method {
		case "initialize":
			response(map[string]string{"userAgent": "fixture"})
		case "model/list":
			modelPages++
			var p struct {
				Cursor string `json:"cursor"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if modelPages > 2 {
				send(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32000, "message": "model catalog was not cached"}})
			} else if p.Cursor == "" {
				response(map[string]any{"data": []modelOption{{Model: "fixture-sol", DisplayName: "Fixture Sol"}, {Model: "hidden-model", Hidden: true}}, "nextCursor": "second"})
			} else {
				response(map[string]any{"data": []modelOption{{Model: "fixture-luna", DisplayName: "Fixture Luna"}}, "nextCursor": nil})
			}
		case "thread/start":
			count++
			active = fmt.Sprintf("thread%d", count)
			response(map[string]any{"thread": map[string]string{"id": active, "cwd": mustGetwd()}})
		case "thread/name/set":
			response(struct{}{})
		case "thread/list":
			response(map[string]any{"data": []map[string]string{{"id": "thread1", "name": "Saved task", "cwd": mustGetwd()}}, "nextCursor": nil})
		case "thread/resume":
			response(map[string]any{"thread": map[string]string{"id": "thread1", "name": "Saved task", "cwd": mustGetwd()}})
		case "thread/turns/list":
			response(map[string]any{"data": []map[string]any{{"id": "old-turn", "status": "completed", "items": []map[string]string{{"id": "old-reply", "type": "agentMessage", "text": "Stored conversation"}}}}, "nextCursor": nil})
		case "turn/start":
			var p struct {
				ThreadID string  `json:"threadId"`
				Input    []input `json:"input"`
				Model    string  `json:"model"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if len(p.Input) > 0 && p.Input[0].Text == "implement feature" && p.Model != "fixture-luna" {
				os.Exit(5)
			}
			active = p.ThreadID
			if len(p.Input) > 0 && p.Input[0].Text == "fail" {
				send(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32000, "message": "fixture failure"}})
				continue
			}
			response(map[string]any{"turn": map[string]string{"id": "turn1", "status": "inProgress"}})
			notify("turn/started", map[string]any{"threadId": active, "turn": map[string]string{"id": "turn1", "status": "inProgress"}})
			notify("item/completed", map[string]any{"threadId": active, "item": map[string]any{"id": "user", "type": "userMessage", "content": p.Input}})
			if len(p.Input) > 0 && p.Input[0].Text == "wait" {
				continue
			}
			notify("item/started", map[string]any{"threadId": active, "item": map[string]string{"id": "cmd", "type": "commandExecution", "command": "go test ./...", "status": "inProgress"}})
			send(map[string]any{"id": 100, "method": "item/commandExecution/requestApproval", "params": map[string]string{"threadId": active, "turnId": "turn1", "itemId": "cmd", "command": "go test ./...", "reason": "fixture approval"}})
		case "turn/interrupt":
			response(struct{}{})
			notify("turn/completed", map[string]any{"threadId": active, "turn": map[string]string{"id": "turn1", "status": "interrupted"}})
		default:
			send(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32601, "message": "unsupported fixture method"}})
		}
	}
}

func mustGetwd() string { p, _ := os.Getwd(); return p }

func TestAgentPEBWorkflow(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PENCODE_CODEX_PATH", path)
	t.Setenv("PENCODE_CODEX_FIXTURE", "1")
	input, hostWrite := io.Pipe()
	hostRead, output := io.Pipe()
	done := make(chan error, 1)
	go func() { err := serveAgent(input, output); _ = output.Close(); done <- err }()
	defer hostWrite.Close()
	defer hostRead.Close()
	frames := make(chan peb.Frame, 64)
	go func() {
		defer close(frames)
		r := peb.Reader{Source: hostRead}
		for {
			f, err := r.Read()
			if err != nil {
				return
			}
			f.Payload = bytes.Clone(f.Payload)
			frames <- f
		}
	}()
	w := peb.Writer{Target: hostWrite}
	write := func(kind peb.Kind, id uint32, data []byte) {
		t.Helper()
		if err := w.Write(kind, id, data); err != nil {
			t.Fatal(err)
		}
	}
	hello, _ := peb.EncodeHello(nil, peb.Hello{Host: "test", Capabilities: peb.Capabilities})
	write(peb.Initialize, 1, hello)
	select {
	case f := <-frames:
		c, err := peb.DecodeContributions(f.Payload)
		if err != nil || f.Kind != peb.Register || len(c.Actions) != 1 || c.Actions[0].Location != peb.StatusBar {
			t.Fatal("invalid registration", f, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("registration timed out")
	}
	write(peb.ActionActivate, 0, nil)
	ctx := peb.ActionContext{ID: "chat", ProjectPath: t.TempDir()}
	id := uint32(1)
	action := func() { id++; data, _ := peb.EncodeActionContext(nil, ctx); write(peb.ActionInvoke, id, data) }
	event := func(surface, kind, ref, value string) {
		id++
		data, err := peb.EncodeUIEvent(nil, peb.UIEvent{ID: surface, Context: ctx, Event: peb.EditorEvent{Kind: kind, Ref: ref, Value: value}})
		if err != nil {
			t.Fatal(err)
		}
		write(peb.UIRequest, id, data)
	}
	waitUI := func(surface, contains string) peb.UI {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case f, ok := <-frames:
				if !ok {
					t.Fatal("extension disconnected")
				}
				if f.Kind == peb.Failure {
					text, _ := peb.DecodeFailure(f.Payload)
					t.Fatal(text)
				}
				if f.Kind != peb.UIUpdate {
					continue
				}
				ui, err := peb.DecodeUI(f.Payload)
				if err != nil {
					t.Fatal(err)
				}
				if ui.View != nil {
					validateBindings(t, ui.View)
				}
				if ui.ID == surface && (contains == "" || ui.View != nil && strings.Contains(ui.View.HTML, contains)) {
					return ui
				}
			case <-deadline:
				t.Fatalf("timed out waiting for %s containing %q", surface, contains)
			}
		}
	}
	action()
	if ui := waitUI("composer", "给 Codex 的任务"); ui.Kind != peb.Panel {
		t.Fatal("composer is not a panel")
	}
	event("composer", "input", "prompt", "implement feature")
	event("composer", "click", "model-settings", "")
	menu := waitUI("models", "Fixture Luna")
	if menu.Kind != peb.ContextMenu || menu.Anchor != "ui:composer#model-settings" || menu.Placement.Mode != peb.PositionAnchorAbove || strings.Contains(menu.View.HTML, "hidden-model") {
		t.Fatal("wrong model catalog or placement", menu)
	}
	event("models", "click", "model/fixture-luna", "")
	if ui := waitUI("composer", "Fixture Luna"); !strings.Contains(ui.View.HTML, "implement feature</textarea>") {
		t.Fatal("selecting a model lost the draft")
	}
	// Reopening uses the cached paginated catalog, and default clears the override.
	event("composer", "click", "model-settings", "")
	menu = waitUI("models", "Fixture Luna")
	if !strings.Contains(menu.View.HTML, `data-ref="model/fixture-luna" aria-checked="true"`) {
		t.Fatal("selected model not marked")
	}
	event("models", "click", "model-default", "")
	waitUI("composer", "><span>默认</span>")
	event("composer", "click", "model-settings", "")
	waitUI("models", "Fixture Luna")
	event("models", "close", "", "")
	waitUI("composer", `aria-label="选择模型" aria-expanded="false"`)
	event("composer", "click", "model-settings", "")
	waitUI("models", "Fixture Luna")
	event("models", "click", "model/fixture-luna", "")
	waitUI("composer", "Fixture Luna")
	event("composer", "click", "send", "")
	if ui := waitUI("thread1", "fixture approval"); ui.Kind != peb.EditorTab || ui.Title != "implement feature" {
		t.Fatal("missing conversation tab", ui)
	}
	event("thread1", "click", "accept/100", "")
	ui := waitUI("thread1", "Final reply")
	if !strings.Contains(ui.View.HTML, "tests passed") || !strings.Contains(ui.View.HTML, "<strong>Final reply</strong> accept") || strings.Contains(ui.View.HTML, "Streamed reply") {
		t.Fatal("completed items did not replace streamed content", ui.View.HTML)
	}
	event("composer", "click", "new", "")
	waitUI("composer", "新会话")
	event("composer", "input", "prompt", "wait")
	event("composer", "click", "send", "")
	waitUI("thread2", "正在工作")
	event("composer", "click", "stop", "")
	waitUI("thread2", "会话记录")
	event("thread1", "close", "", "")
	event("composer", "click", "history", "")
	waitUI("composer", "Saved task")
	event("composer", "click", "history/thread1", "")
	waitUI("thread1", "Stored conversation")
	// A failed turn must preserve the user's input instead of silently losing it.
	event("composer", "input", "prompt", "fail")
	event("composer", "click", "send", "")
	ui = waitUI("composer", "fixture failure")
	if !strings.Contains(ui.View.HTML, ">fail</textarea>") {
		t.Fatal("failed request lost the draft")
	}
	write(peb.Shutdown, 0, nil)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("extension did not reap app-server on shutdown")
	}
}

var refPattern = regexp.MustCompile(`data-ref="([^"]+)"`)

func validateBindings(t *testing.T, v *peb.EditorView) {
	t.Helper()
	refs := make(map[string]bool)
	for _, m := range refPattern.FindAllStringSubmatch(v.HTML, -1) {
		ref := html.UnescapeString(m[1])
		if refs[ref] {
			t.Fatalf("duplicate ref %s", m[1])
		}
		refs[ref] = true
	}
	for _, binding := range v.Events {
		if !refs[binding.Ref] {
			t.Fatalf("binding %s has no control", binding.Ref)
		}
	}
}

func TestHistoryResumeAndReversePagination(t *testing.T) {
	path, _ := os.Executable()
	t.Setenv("PENCODE_CODEX_PATH", path)
	t.Setenv("PENCODE_CODEX_FIXTURE", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &agent{ctx: ctx, context: peb.ActionContext{ProjectPath: t.TempDir()}, conversations: make(map[string]*conversation)}
	if err := a.connect(); err != nil {
		t.Fatal(err)
	}
	defer a.client.close()
	// Load a stored thread without publishing UI; verify history is decoded and
	// remains in chronological order when prepended to existing live messages.
	c := &conversation{thread: thread{ID: "thread1"}, context: a.context}
	if err := a.loadHistory(c); err != nil {
		t.Fatal(err)
	}
	if len(c.items) != 1 || c.items[0].text.String() != "Stored conversation" {
		t.Fatalf("wrong history: %+v", c.items)
	}
	if err := a.listHistory(""); err != nil || len(a.history.Data) != 1 {
		t.Fatal(err, a.history)
	}
}

func TestMarkdownIsEscapedAndDoesNotLoadImages(t *testing.T) {
	var b strings.Builder
	renderMarkdown(&b, "**safe**\n\n<script>alert(1)</script>\n\n![secret](https://example.com/secret)\n\nafter image")
	if !strings.Contains(b.String(), "<strong>safe</strong>") || strings.Contains(b.String(), "<script>") || strings.Contains(b.String(), "<img") || !strings.Contains(b.String(), "after image") {
		t.Fatal(b.String())
	}
	c := &conversation{thread: thread{ID: "test", Name: `<script>bad</script>`}, draft: `<img src="bad">`}
	a := &agent{selected: c, status: `<script>error</script>`}
	view := a.composerView()
	validateBindings(t, view)
	if strings.Contains(view.HTML, "<script>") || strings.Contains(view.HTML, `<img src="bad">`) {
		t.Fatal("unescaped user content")
	}
}

func TestBoundedStreamAndIdleAllocations(t *testing.T) {
	var b strings.Builder
	b.Grow(128 << 10)
	if n := testing.AllocsPerRun(1000, func() { appendText(&b, "hello") }); n != 0 {
		t.Fatalf("warm deltas allocate: %g", n)
	}
	appendText(&b, strings.Repeat("中", 128<<10))
	if b.Len() > 128<<10 || !utf8.ValidString(b.String()) {
		t.Fatal("stream did not respect the UTF-8 display budget")
	}
	a := &agent{conversations: map[string]*conversation{"x": {}}}
	if n := testing.AllocsPerRun(1000, func() {
		if a.dirty() {
			panic("idle")
		}
	}); n != 0 {
		t.Fatalf("idle dirty check allocated %g", n)
	}
}

func TestLiveAppServer(t *testing.T) {
	if os.Getenv("PENCODE_CODEX_INTEGRATION") != "1" {
		t.Skip("set PENCODE_CODEX_INTEGRATION=1 for a local CLI smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	a := &agent{ctx: ctx, context: peb.ActionContext{ProjectPath: t.TempDir()}, conversations: make(map[string]*conversation)}
	if err := a.loadModels(); err != nil {
		t.Fatal("model list unavailable", err)
	}
	defer a.client.close()
	if len(a.models) == 0 || !a.modelsLoaded {
		t.Fatal("empty model catalog")
	}
	validateBindings(t, a.modelsView())
	root := t.TempDir()
	started, err := call[struct {
		Thread thread `json:"thread"`
	}](a.client, "thread/start", map[string]any{"cwd": root, "ephemeral": true, "sandbox": "workspace-write", "approvalPolicy": "on-request"})
	if err != nil || started.Thread.ID == "" {
		t.Fatal("ephemeral thread/start unavailable", err)
	}
	t.Log("real app-server handshake, model/list and ephemeral thread/start succeeded; no turn submitted")
}
