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
	"path/filepath"
	"regexp"
	"slices"
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
	threadPermissions := make(map[string]string)
	titleThreads := make(map[string]bool)
	names := make(map[string]string)
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
		response := func(v any) {
			if page, ok := v.(threadPage); ok {
				for i := range page.Data {
					if name := names[page.Data[i].ID]; name != "" {
						page.Data[i].Name = name
					}
				}
				v = page
			}
			send(map[string]any{"id": msg.ID, "result": v})
		}
		if msg.Method != "initialize" && !initialized {
			os.Exit(3)
		}
		if msg.Method == "thread/list" || msg.Method == "thread/turns/list" {
			var p struct {
				Cursor *string `json:"cursor"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.Cursor != nil && *p.Cursor != "second" {
				send(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32600, "message": "invalid cursor: " + *p.Cursor}})
				continue
			}
		}
		var permissions struct {
			Sandbox           string `json:"sandbox"`
			ApprovalPolicy    string `json:"approvalPolicy"`
			ApprovalsReviewer string `json:"approvalsReviewer"`
			SandboxPolicy     struct {
				Type          string `json:"type"`
				NetworkAccess bool   `json:"networkAccess"`
			} `json:"sandboxPolicy"`
		}
		_ = json.Unmarshal(msg.Params, &permissions)
		preset := permissions.ApprovalPolicy + "/" + permissions.ApprovalsReviewer + "/"
		switch msg.Method {
		case "initialize":
			fixtureWait("PENCODE_CODEX_STARTUP_GATE")
			response(map[string]string{"userAgent": "fixture"})
		case "config/read":
			response(map[string]any{"config": map[string]any{"mcp_servers": map[string]any{"fixture": map[string]bool{"enabled": true}}}})
		case "model/list":
			modelPages++
			var p struct {
				Cursor string `json:"cursor"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if modelPages > 2 {
				send(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32000, "message": "model catalog was not cached"}})
			} else if p.Cursor == "" {
				response(map[string]any{"data": []modelOption{{Model: "fixture-sol", DisplayName: "Fixture Sol", IsDefault: true, DefaultReasoningEffort: "low"}, {Model: "hidden-model", Hidden: true}}, "nextCursor": "second"})
			} else {
				response(map[string]any{"data": []modelOption{{Model: "fixture-luna", DisplayName: "Fixture Luna", DefaultReasoningEffort: "medium", SupportedReasoningEfforts: []reasoningEffortOption{{"low"}, {"medium"}, {"high"}}}}, "nextCursor": nil})
			}
		case "thread/start":
			var p struct {
				Ephemeral        bool                       `json:"ephemeral"`
				Model            string                     `json:"model"`
				Config           map[string]json.RawMessage `json:"config"`
				BaseInstructions string                     `json:"baseInstructions"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.Ephemeral && p.Model != "gpt-6-luna" {
				os.Exit(8)
			}
			if p.Ephemeral && (permissions.Sandbox != "read-only" || permissions.ApprovalPolicy != "never" || p.BaseInstructions != titleInstructions || string(p.Config["mcp_servers"]) != `{"fixture":{"enabled":false}}` || string(p.Config["features.shell_tool"]) != "false") {
				os.Exit(6)
			}
			count++
			active = fmt.Sprintf("thread%d", count)
			titleThreads[active] = p.Ephemeral
			threadPermissions[active] = preset + permissions.Sandbox
			response(map[string]any{"thread": map[string]string{"id": active, "cwd": mustGetwd()}})
		case "thread/name/set":
			var p struct {
				ThreadID string `json:"threadId"`
				Name     string `json:"name"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.Name == "reject name" {
				send(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32000, "message": "rename failed"}})
				continue
			}
			names[p.ThreadID] = p.Name
			notify("thread/name/updated", map[string]string{"threadId": p.ThreadID, "threadName": p.Name})
			response(struct{}{})
		case "thread/list":
			fixtureWait("PENCODE_CODEX_HISTORY_GATE")
			var p struct {
				Cursor         string   `json:"cursor"`
				Cwd            string   `json:"cwd"`
				SourceKinds    []string `json:"sourceKinds"`
				UseStateDBOnly bool     `json:"useStateDbOnly"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if !p.UseStateDBOnly {
				fixtureWait("PENCODE_CODEX_HISTORY_SCAN_GATE")
			}
			if !filepath.IsAbs(p.Cwd) || !slices.Equal(p.SourceKinds, []string{"cli", "vscode", "appServer"}) {
				send(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32602, "message": "history must specify project and interactive sources"}})
				continue
			}
			if os.Getenv("PENCODE_CODEX_HISTORY_PROJECTS") == "1" {
				response(threadPage{Data: []thread{{ID: p.Cwd + "/saved", Name: filepath.Base(p.Cwd) + " task"}}})
			} else if os.Getenv("PENCODE_CODEX_HISTORY_PAGES") == "1" {
				if p.Cursor == "" {
					response(threadPage{Data: []thread{{ID: "saved", Name: "Saved task"}, {ID: "saved", Name: "Saved task"}, {}}, NextCursor: "second"})
				} else {
					response(threadPage{Data: []thread{{ID: "thread1", Name: "Indexed task"}, {ID: "older", Name: "Older task"}, {ID: "thread1", Name: "Indexed task"}, {ID: "older", Name: "Older task"}}})
				}
			} else {
				response(threadPage{Data: []thread{{ID: "saved", Name: "Saved task"}}})
			}
		case "thread/resume":
			var p struct {
				ThreadID string `json:"threadId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			threadPermissions[p.ThreadID] = preset + permissions.Sandbox
			name := names[p.ThreadID]
			if name == "" {
				name = "Saved task"
			}
			response(map[string]any{"thread": map[string]string{"id": p.ThreadID, "name": name, "cwd": mustGetwd()}})
		case "thread/turns/list":
			var p struct {
				Cursor string `json:"cursor"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			var nextCursor any
			itemID, text := "old-reply", "Stored conversation"
			if os.Getenv("PENCODE_CODEX_TURN_PAGES") == "1" {
				if p.Cursor == "" {
					nextCursor = "second"
				} else {
					itemID, text = "older-reply", "Older conversation"
				}
			}
			stored := map[string]any{"id": "old-turn", "status": "completed", "items": []map[string]string{{"id": itemID, "type": "agentMessage", "text": text}}}
			if mode := os.Getenv("PENCODE_CODEX_TURN_CHANGES"); mode != "" {
				stored["items"] = []threadItem{
					{ID: "stored-edit", Type: "fileChange", Status: "completed", Changes: []change{{Path: "saved.go", Diff: "+stored\n"}}},
					{ID: itemID, Type: "agentMessage", Text: text},
				}
				if mode == "running" {
					stored["status"] = "inProgress"
				}
			}
			if os.Getenv("PENCODE_CODEX_TURN_DURATION") == "1" {
				stored["durationMs"] = 112000
			} else if os.Getenv("PENCODE_CODEX_TURN_DURATION") == "started-only" {
				stored["startedAt"] = 100
			}
			response(map[string]any{"data": []map[string]any{stored}, "nextCursor": nextCursor})
		case "turn/start":
			var p struct {
				ThreadID string  `json:"threadId"`
				Input    []input `json:"input"`
				Model    string  `json:"model"`
				Effort   string  `json:"effort"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if titleThreads[p.ThreadID] {
				if p.Effort != "low" {
					os.Exit(9)
				}
				fixtureWait("PENCODE_CODEX_TITLE_GATE")
				var schema struct {
					OutputSchema json.RawMessage `json:"outputSchema"`
				}
				_ = json.Unmarshal(msg.Params, &schema)
				if len(schema.OutputSchema) == 0 {
					os.Exit(7)
				}
				text, status := `{"title":"实现功能"}`, "completed"
				if p.Input[0].Text == "invalid title" {
					text = "not json"
				} else if p.Input[0].Text == "empty title" {
					text = `{"title":"   "}`
				} else if p.Input[0].Text == "title failure" {
					status = "failed"
				}
				response(map[string]any{"turn": map[string]string{"id": "title-turn", "status": "inProgress"}})
				notify("item/completed", map[string]any{"threadId": p.ThreadID, "item": map[string]string{"id": "title", "type": "agentMessage", "text": text}})
				notify("turn/completed", map[string]any{"threadId": p.ThreadID, "turn": map[string]string{"id": "title-turn", "status": status}})
				continue
			}
			if len(p.Input) > 0 && p.Input[0].Text == "implement feature" && (p.Model != "fixture-luna" || p.Effort != "high") {
				os.Exit(5)
			}
			active = p.ThreadID
			notify("item/completed", map[string]any{"threadId": active, "item": map[string]string{"id": "permissions", "type": "agentMessage", "text": "Thread permissions: " + threadPermissions[active] + "; Turn permissions: " + preset + permissions.SandboxPolicy.Type + fmt.Sprintf("; network=%t; Effort: %s", permissions.SandboxPolicy.NetworkAccess, p.Effort)}})
			if len(p.Input) > 0 && (p.Input[0].Text == "fail" || strings.HasPrefix(p.Input[0].Text, "fail\n\n附加文件")) {
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
			var p struct {
				ThreadID string `json:"threadId"`
				TurnID   string `json:"turnId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			if p.ThreadID != active || p.TurnID != "turn1" {
				send(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32602, "message": "interrupt targeted the wrong task"}})
				continue
			}
			response(struct{}{})
			notify("turn/completed", map[string]any{"threadId": active, "turn": map[string]string{"id": "turn1", "status": "interrupted"}})
		default:
			send(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32601, "message": "unsupported fixture method"}})
		}
	}
	fixtureWait("PENCODE_CODEX_SHUTDOWN_GATE")
	if os.Getenv("PENCODE_CODEX_SHUTDOWN_OUTPUT") == "1" {
		for range 256 {
			notify("thread/closed", strings.Repeat("x", 4096))
		}
	}
}

func fixtureWait(name string) {
	if gate := os.Getenv(name); gate != "" {
		for {
			if _, err := os.Stat(gate); err == nil {
				return
			}
			if err := os.WriteFile(gate+".waiting", nil, 0o600); err != nil {
				os.Exit(2)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func mustGetwd() string { p, _ := os.Getwd(); return p }

func TestAgentPEBWorkflow(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PENCODE_CODEX_PATH", filepath.Join(t.TempDir(), "missing-codex"))
	t.Setenv("PENCODE_CODEX_FIXTURE", "1")
	gate := filepath.Join(t.TempDir(), "start-codex")
	t.Setenv("PENCODE_CODEX_STARTUP_GATE", gate)
	historyGate := filepath.Join(t.TempDir(), "read-history")
	t.Setenv("PENCODE_CODEX_HISTORY_GATE", historyGate)
	if err := os.WriteFile(historyGate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
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
		if len(c.Settings) != 1 || c.Settings[0].ID != "executablePath" || c.Settings[0].Type != peb.SettingString || string(c.Settings[0].Default) != `""` {
			t.Fatal("missing executable path setting", c.Settings)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("registration timed out")
	}
	pathValue, _ := json.Marshal(path)
	settings, _ := peb.EncodeSettingValues(nil, peb.SettingValues{"executablePath": pathValue})
	write(peb.SettingsRequest, 2, settings)
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
	var views peb.ViewReceiver
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
				ui, err = views.Apply(ui)
				if err != nil {
					t.Fatal(err)
				}
				if ui.Kind == peb.Panel || ui.ID != "composer" && ui.ID != "models" && ui.ID != "permissions" && ui.ID != "files" && ui.ID != "rename" && ui.ID != "error" {
					t.Fatal("chat opened an extra surface", ui)
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
	// The editor tab must open even while Codex has not acknowledged startup.
	if ui := waitUI("composer", "给 Codex 的任务"); ui.Kind != peb.EditorTab {
		t.Fatal("chat did not open before Codex initialized")
	}
	// Closing and reopening must also work while app-server startup is pending.
	event("composer", "input", "prompt", "startup draft")
	for range 2 {
		event("composer", "close", "", "")
		action()
		if ui := waitUI("composer", "给 Codex 的任务"); ui.Kind != peb.EditorTab {
			t.Fatal("chat did not reopen while Codex initialized")
		}
	}
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if ui := waitUI("composer", "Saved task"); ui.Kind != peb.EditorTab || !strings.Contains(ui.View.HTML, "startup draft") || strings.Contains(ui.View.HTML, `class="codex-history-spinning"`) {
		t.Fatal("chat workspace is not an editor tab")
	}
	// Invoking the entry always opens or reveals, even before a close event arrives.
	action()
	if ui := waitUI("composer", ""); ui.Close || ui.View == nil {
		t.Fatal("Codex entry did not publish its workspace", ui)
	}
	if ui := waitUI("composer", ""); ui.Close || ui.View != nil {
		t.Fatal("Codex entry did not reveal its existing tab", ui)
	}
	event("composer", "input", "prompt", "implement feature")
	attachment := filepath.Join(t.TempDir(), "extra notes.txt")
	if err := os.WriteFile(attachment, []byte("file context"), 0o600); err != nil {
		t.Fatal(err)
	}
	addFile := func(path string) {
		t.Helper()
		event("composer", "click", "add-file", "")
		picker := waitUI("files", "文件或目录路径")
		if picker.Kind != peb.Dialog || strings.Contains(picker.View.HTML, "新会话") || strings.Contains(picker.View.HTML, "历史会话") {
			t.Fatal("plus should open a file picker", picker)
		}
		event("files", "input", "file-path", path)
		event("files", "submit", "file-path-form", "")
		ui := waitUI("composer", `data-ref="remove-file/0"`)
		if !strings.Contains(ui.View.HTML, html.EscapeString(filepath.Base(path))) {
			t.Fatal("file not added to composer", ui.View.HTML)
		}
	}
	addFile(attachment)
	event("composer", "click", "remove-file/0", "")
	if ui := waitUI("composer", "implement feature</textarea>"); strings.Contains(ui.View.HTML, "codex-attachments") {
		t.Fatal("removing an attachment lost the draft or retained the file", ui.View.HTML)
	}
	addFile(attachment)
	event("composer", "click", "permission-settings", "")
	permissions := waitUI("permissions", "完全访问权限")
	if permissions.Kind != peb.ContextMenu || permissions.Anchor != "ui:composer#permission-settings" || permissions.Placement.Mode != peb.PositionAnchorAbove || !strings.Contains(permissions.View.HTML, `data-ref="permission/ask" aria-checked="true"`) {
		t.Fatal("wrong permission menu or default", permissions)
	}
	event("permissions", "click", "permission-help", "")
	waitUI("permissions", "设置从下一次发送开始生效")
	event("permissions", "close", "", "")
	waitUI("composer", `aria-label="权限设置" aria-expanded="false"`)
	event("composer", "click", "model-settings", "")
	menu := waitUI("models", "Fixture Luna")
	if menu.Kind != peb.ContextMenu || menu.Anchor != "ui:composer#model-settings" || menu.Placement.Mode != peb.PositionAnchorAbove || strings.Contains(menu.View.HTML, "hidden-model") {
		t.Fatal("wrong model catalog or placement", menu)
	}
	event("models", "click", "model/fixture-luna", "")
	menu = waitUI("models", "选择推理强度")
	if !strings.Contains(menu.View.HTML, `data-ref="effort/medium" aria-checked="true"`) {
		t.Fatal("model default effort not marked", menu.View.HTML)
	}
	event("models", "click", "models-back", "")
	menu = waitUI("models", "推荐模型集")
	if !strings.Contains(menu.View.HTML, `data-ref="model-default" aria-checked="true"`) {
		t.Fatal("going back applied the pending model", menu.View.HTML)
	}
	event("models", "click", "model/fixture-luna", "")
	waitUI("models", "选择推理强度")
	event("models", "close", "", "")
	if ui := waitUI("composer", `aria-label="选择模型" aria-expanded="false"`); !strings.Contains(ui.View.HTML, "><span>默认</span>") {
		t.Fatal("closing effort selection applied the pending model", ui.View.HTML)
	}
	event("composer", "click", "model-settings", "")
	waitUI("models", "Fixture Sol")
	event("models", "click", "model/fixture-sol", "")
	waitUI("composer", "Fixture Sol") // Models without effort options complete directly.
	event("composer", "click", "model-settings", "")
	waitUI("models", "Fixture Luna")
	event("models", "click", "model/fixture-luna", "")
	waitUI("models", "选择推理强度")
	event("models", "click", "effort/high", "")
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
	menu = waitUI("models", "选择推理强度")
	if !strings.Contains(menu.View.HTML, `data-ref="effort/medium" aria-checked="true"`) {
		t.Fatal("model default effort not marked", menu.View.HTML)
	}
	event("models", "click", "effort/high", "")
	waitUI("composer", "Fixture Luna")
	event("composer", "click", "send", "")
	// The first paint must already offer Stop, before the server acknowledges.
	firstUpdate := waitUI("composer", "给 Codex 的任务")
	if !firstUpdate.Busy {
		t.Fatal("tab must show processing before turn acknowledgement")
	}
	firstPaint := firstUpdate.View
	checkComposerAction(t, firstPaint, true)
	if !strings.Contains(firstPaint.HTML, "正在思考") {
		t.Fatal("first paint must show thinking before turn acknowledgement", firstPaint.HTML)
	}
	if !strings.Contains(firstPaint.HTML, `data-ref="history/thread1" aria-current="true">Codex 新会话</button>`) {
		t.Fatal("new conversation missing from history before turn acknowledgement", firstPaint.HTML)
	}
	if ui := waitUI("composer", "fixture approval"); ui.Kind != peb.EditorTab || ui.Title == "implement feature" {
		t.Fatal("missing conversation tab", ui)
	} else if !strings.Contains(ui.View.HTML, "Thread permissions: on-request/user/workspace-write; Turn permissions: on-request/user/workspaceWrite; network=false; Effort: high") {
		t.Fatal("default permissions were not sent", ui.View.HTML)
	} else if !strings.Contains(ui.View.HTML, html.EscapeString(attachmentPrompt("implement feature", []string{attachment}))) || strings.Contains(ui.View.HTML, `data-ref="remove-file/0"`) {
		t.Fatal("attachment was not sent or not cleared after success", ui.View.HTML)
	}
	event("composer", "click", "accept/100", "")
	ui := waitUI("composer", "Final reply")
	if ui.Busy {
		t.Fatal("completed turn retained the tab processing icon")
	}
	checkComposerAction(t, ui.View, false)
	if strings.Contains(ui.View.HTML, "tests passed") {
		t.Fatal("command details should start collapsed", ui.View.HTML)
	}
	event("composer", "click", "refresh-history", "")
	if ui := waitUI("composer", "实现功能</button>"); ui.Title != "实现功能" {
		t.Fatal("generated title did not update the tab and history", ui)
	}
	event("composer", "click", "rename/thread1", "")
	validateBindings(t, waitUI("rename", `value="实现功能"`).View)
	event("rename", "input", "rename-title", "   ")
	event("rename", "submit", "rename-form", "")
	waitUI("error", "会话标题不能为空")
	event("rename", "input", "rename-title", "reject name")
	event("rename", "submit", "rename-form", "")
	waitUI("error", "rename failed")
	event("rename", "input", "rename-title", " 手动标题 <任务> ")
	event("rename", "submit", "rename-form", "")
	if ui := waitUI("composer", "手动标题 &lt;任务&gt;</button>"); ui.Title != "手动标题 <任务>" {
		t.Fatal("manual title did not update the tab", ui)
	}
	// Renaming an unopened history entry must not change the selected chat.
	event("composer", "click", "rename/saved", "")
	waitUI("rename", `value="Saved task"`)
	event("rename", "input", "rename-title", "历史标题")
	event("rename", "submit", "rename-form", "")
	if ui := waitUI("composer", "历史标题</button>"); ui.Title != "手动标题 <任务>" {
		t.Fatal("renaming history changed the selected conversation", ui)
	}
	event("composer", "click", "rename/saved", "")
	waitUI("rename", `value="历史标题"`)
	event("rename", "click", "rename-cancel", "")
	event("composer", "click", "activity/cmd", "")
	waitUI("composer", `data-ref="item/cmd" aria-expanded="false"`)
	event("composer", "click", "item/cmd", "")
	ui = waitUI("composer", "tests passed")
	if !strings.Contains(ui.View.HTML, "tests passed") || !strings.Contains(ui.View.HTML, "<strong>Final reply</strong> accept") || strings.Contains(ui.View.HTML, "Streamed reply") {
		t.Fatal("completed items did not replace streamed content", ui.View.HTML)
	}
	event("composer", "click", "item/cmd", "")
	ui = waitUI("composer", `data-ref="item/cmd" aria-expanded="false"`)
	if strings.Contains(ui.View.HTML, "tests passed") {
		t.Fatal("command details did not collapse", ui.View.HTML)
	}
	event("composer", "click", "model-settings", "")
	waitUI("models", "Fixture Luna")
	event("models", "click", "model/fixture-luna", "")
	menu = waitUI("models", "选择推理强度")
	if !strings.Contains(menu.View.HTML, `data-ref="effort/high" aria-checked="true"`) {
		t.Fatal("saved effort not marked", menu.View.HTML)
	}
	event("models", "click", "effort/low", "")
	waitUI("composer", `codex-model-effort">低</span>`)
	event("composer", "click", "new", "")
	waitUI("composer", "新会话")
	event("composer", "input", "prompt", "wait")
	event("composer", "click", "model-settings", "")
	waitUI("models", "Fixture Luna")
	event("composer", "click", "permission-settings", "")
	waitUI("models", "") // Opening permissions closes the model menu.
	waitUI("permissions", "完全访问权限")
	event("permissions", "click", "permission/auto", "")
	if ui := waitUI("composer", "帮我批准"); !strings.Contains(ui.View.HTML, ">wait</textarea>") || !strings.Contains(ui.View.HTML, `aria-label="选择模型" aria-expanded="false"`) {
		t.Fatal("permission selection lost the draft or left models open", ui.View.HTML)
	}
	event("composer", "click", "send", "")
	ui = waitUI("composer", "Thread permissions: on-request/auto_review/workspace-write; Turn permissions: on-request/auto_review/workspaceWrite; network=false; Effort: low")
	checkComposerAction(t, ui.View, true)
	// Changing the preset while busy applies only to the next turn.
	event("composer", "click", "permission-settings", "")
	waitUI("permissions", `data-ref="permission/auto" aria-checked="true"`)
	event("permissions", "click", "permission/full", "")
	waitUI("composer", "完全访问</span>")
	event("composer", "click", "stop", "")
	checkComposerAction(t, waitUI("composer", "任务已停止").View, false)
	event("composer", "close", "", "")
	action()
	waitUI("composer", "给 Codex 的任务")
	event("composer", "click", "history", "")
	waitUI("composer", "历史标题")
	event("composer", "click", "history/saved", "")
	waitUI("composer", "Stored conversation")
	// Closing exits the conversation even while history is slow; it remains
	// available in history and retains its draft when explicitly resumed.
	if err := os.Remove(historyGate); err != nil {
		t.Fatal(err)
	}
	event("composer", "input", "prompt", "draft while history loads")
	event("composer", "click", "refresh-history", "")
	if ui := waitUI("composer", `class="codex-history-spinning"`); !strings.Contains(ui.View.HTML, `disabled aria-busy="true"`) || !strings.Contains(ui.View.HTML, "Stored conversation") {
		t.Fatal("pending refresh did not retain the chat and show loading", ui)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(historyGate + ".waiting"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("history request did not start")
		}
		time.Sleep(time.Millisecond)
	}
	event("composer", "close", "", "")
	action()
	if ui := waitUI("composer", "给 Codex 的任务"); strings.Contains(ui.View.HTML, "Stored conversation") || ui.Busy {
		t.Fatal("reopening retained the exited conversation", ui)
	}
	if err := os.WriteFile(historyGate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	event("composer", "click", "history/saved", "")
	if ui := waitUI("composer", "draft while history loads"); strings.Contains(ui.View.HTML, `class="codex-history-spinning"`) {
		t.Fatal("completed refresh retained the loading indicator", ui)
	}
	// A failed turn must preserve the user's input instead of silently losing it.
	event("composer", "input", "prompt", "fail")
	addFile(attachment)
	event("composer", "click", "send", "")
	if notice := waitUI("error", "fixture failure"); notice.Kind != peb.Notification || notice.Context.ProjectPath != ctx.ProjectPath {
		t.Fatal("failed request did not use an IDE notification", notice)
	}
	ui = waitUI("composer", `data-ref="send"`)
	if strings.Contains(ui.View.HTML, "fixture failure") {
		t.Fatal("failed request leaked into the composer", ui.View.HTML)
	}
	checkComposerAction(t, ui.View, false)
	if !strings.Contains(ui.View.HTML, "Thread permissions: never/user/danger-full-access; Turn permissions: never/user/dangerFullAccess") {
		t.Fatal("resumed turn lost full access", ui.View.HTML)
	}
	if !strings.Contains(ui.View.HTML, ">fail</textarea>") {
		t.Fatal("failed request lost the draft")
	}
	if !strings.Contains(ui.View.HTML, `data-ref="remove-file/0"`) {
		t.Fatal("failed request lost the attachment")
	}
	event("composer", "click", "remove-file/0", "")
	if ui := waitUI("composer", ">fail</textarea>"); strings.Contains(ui.View.HTML, `data-ref="remove-file/0"`) {
		t.Fatal("attachment removal failed", ui.View.HTML)
	}
	// Downgrading a loaded conversation must replace its full-access turn policy.
	event("composer", "click", "permission-settings", "")
	waitUI("permissions", `data-ref="permission/full" aria-checked="true"`)
	event("permissions", "click", "permission/ask", "")
	waitUI("composer", "请批准</span>")
	event("composer", "input", "prompt", "wait")
	event("composer", "click", "send", "")
	waitUI("composer", "Thread permissions: never/user/danger-full-access; Turn permissions: on-request/user/workspaceWrite; network=false; Effort: low")
	event("composer", "click", "stop", "")
	checkComposerAction(t, waitUI("composer", "任务已停止").View, false)
	// Full access also reaches newly created threads, not only resumed ones.
	event("composer", "click", "new", "")
	waitUI("composer", "新会话")
	event("composer", "click", "permission-settings", "")
	waitUI("permissions", "完全访问权限")
	event("permissions", "click", "permission/full", "")
	waitUI("composer", "完全访问</span>")
	event("composer", "input", "prompt", "wait")
	event("composer", "click", "send", "")
	waitUI("composer", "Thread permissions: never/user/danger-full-access; Turn permissions: never/user/dangerFullAccess")
	event("composer", "click", "permission-settings", "")
	waitUI("permissions", "完全访问权限")
	event("composer", "visibilitychange", "", "hidden")
	if ui := waitUI("permissions", ""); !ui.Close {
		t.Fatal("hidden composer left permission menu open")
	}
	// Menu selections must reach disk and restore without this agent's state.
	restarted := &agent{}
	if err := restarted.setContext(ctx); err != nil || restarted.model != "fixture-luna" || restarted.effort != "low" || restarted.permissionIndex != 2 {
		t.Fatalf("menu selections were not persisted: model=%q permission=%d err=%v", restarted.model, restarted.permissionIndex, err)
	}
	// Invoking Codex from a file reveals its existing tab.
	action()
	waitUI("composer", "给 Codex 的任务")
	if ui := waitUI("composer", ""); ui.View != nil || ui.Close {
		t.Fatal("hidden chat was not revealed", ui)
	}
	event("composer", "close", "", "")
	projectContext := ctx
	ctx.ProjectPath = t.TempDir()
	action()
	ui = waitUI("composer", "请批准</span>")
	if !strings.Contains(ui.View.HTML, "><span>默认</span>") || strings.Contains(ui.View.HTML, "Fixture Luna") {
		t.Fatal("new project inherited model settings", ui.View.HTML)
	}
	event("composer", "close", "", "")
	ctx = projectContext
	action()
	ui = waitUI("composer", "完全访问</span>")
	if !strings.Contains(ui.View.HTML, "Fixture Luna") {
		t.Fatal("returning to project did not restore model", ui.View.HTML)
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

func checkComposerAction(t *testing.T, view *peb.EditorView, running bool) {
	t.Helper()
	ref, icon, other := "send", "arrow-up", "stop"
	if running {
		ref, icon, other = "stop", "square", "send"
	}
	if !strings.Contains(view.HTML, `data-ref="`+ref+`" class="codex-icon-button codex-send"`) ||
		!strings.Contains(view.HTML, `src="resources/icons/`+icon+`.svg"`) || strings.Contains(view.HTML, `data-ref="`+other+`"`) {
		t.Fatalf("wrong composer action for running=%t: %s", running, view.HTML)
	}
	for _, binding := range view.Events {
		if binding.Ref == ref && binding.Kind == "click" {
			return
		}
	}
	t.Fatalf("missing click binding for %s", ref)
}

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
	t.Setenv("PENCODE_CODEX_TURN_PAGES", "1")
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
	if len(c.items) != 1 || c.items[0].text.String() != "Stored conversation" || c.cursor != "second" {
		t.Fatalf("wrong history: %+v", c.items)
	}
	if err := a.loadHistory(c); err != nil {
		t.Fatal(err)
	}
	if len(c.items) != 2 || c.items[0].text.String() != "Older conversation" || c.items[1].text.String() != "Stored conversation" || c.cursor != "" {
		t.Fatalf("wrong paginated history: %+v, cursor %q", c.items, c.cursor)
	}
	if err := a.listHistory(""); err != nil || len(a.history.Data) != 1 {
		t.Fatal(err, a.history)
	}
}

func TestHistoryRetainsNewThreadsAndDeduplicatesPages(t *testing.T) {
	path, _ := os.Executable()
	t.Setenv("PENCODE_CODEX_PATH", path)
	t.Setenv("PENCODE_CODEX_FIXTURE", "1")
	t.Setenv("PENCODE_CODEX_HISTORY_PAGES", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	a := &agent{ctx: ctx, conversations: make(map[string]*conversation)}
	if err := a.setContext(peb.ActionContext{ProjectPath: root}); err != nil {
		t.Fatal(err)
	}
	if err := a.listHistory(""); err != nil {
		t.Fatal(err)
	}
	defer a.client.close()
	if !a.showHistory || len(a.history.Data) != 1 || a.history.Data[0].ID != "saved" {
		t.Fatal("listing history did not load the saved thread", a.history)
	}
	c, err := a.startThread()
	if err != nil {
		t.Fatal(err)
	}
	if a.history.Data[0] != c.thread {
		t.Fatal("new thread was not added to the top of history", a.history)
	}
	a.conversations["foreign"] = &conversation{thread: thread{ID: "foreign"}, context: peb.ActionContext{ProjectPath: t.TempDir()}}
	if err := a.listHistory(""); err != nil {
		t.Fatal(err)
	}
	if len(a.history.Data) != 2 || a.history.Data[0] != c.thread || a.history.contains("foreign") {
		t.Fatal("refresh lost a new thread or included another project's thread", a.history)
	}
	if err := a.listHistory(a.history.NextCursor); err != nil {
		t.Fatal(err)
	}
	if len(a.history.Data) != 3 || a.history.Data[0] != c.thread || a.history.Data[2].ID != "older" || a.history.NextCursor != "" {
		t.Fatal("pagination duplicated a local thread or reordered history", a.history)
	}
	validateBindings(t, a.composerView())
	if allocations := testing.AllocsPerRun(100, func() { a.history.include(c.thread) }); allocations != 0 {
		t.Fatalf("including a known thread allocates: %g", allocations)
	}
	if err := a.setContext(peb.ActionContext{ProjectPath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if err := a.listHistory(""); err != nil {
		t.Fatal(err)
	}
	if a.history.contains(c.ID) || len(a.history.Data) != 1 {
		t.Fatal("switching projects retained local history from another project", a.history)
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
	for _, m := range a.models {
		if len(m.SupportedReasoningEfforts) != 0 {
			a.pendingModel = m.Model
			validateBindings(t, a.modelsView())
		}
	}
	a.pendingModel = ""
	if err := a.listHistory(""); err != nil {
		t.Fatal("state database thread/list unavailable", err)
	}
	root := t.TempDir()
	started, err := call[struct {
		Thread thread `json:"thread"`
	}](a.client, "thread/start", map[string]any{"cwd": root, "ephemeral": true, "sandbox": "workspace-write", "approvalPolicy": "on-request"})
	if err != nil || started.Thread.ID == "" {
		t.Fatal("ephemeral thread/start unavailable", err)
	}
	t.Log("real app-server handshake, model/list, thread/list and ephemeral thread/start succeeded; no turn submitted")
}
