package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pencode-labs/pencode/peb"
)

func TestContinueInterruptedHistory(t *testing.T) {
	client, output := snapshotClient(t,
		turnPage{Data: []turn{{ID: "stopped", Status: "interrupted"}}, NextCursor: "older"},
		turnPage{Data: []turn{{ID: "older", Status: "completed"}}},
	)
	c := &conversation{thread: thread{ID: "thread"}, loaded: true}
	a := &agent{selected: c, client: client}
	for range 2 {
		if err := a.readHistory(c); err != nil {
			t.Fatal(err)
		}
		checkComposerAction(t, a.composerView(), "continue")
	}
	output.Reset()
	click := command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "click", Ref: "continue"}}}
	c.dirty = false
	a.setDraft("改做另一项任务")
	if !c.dirty {
		t.Fatal("typing after stopping must refresh the button")
	}
	checkComposerAction(t, a.composerView(), "send")
	if err := a.handleCommand(click); err != nil || output.Len() != 0 || c.draft != "改做另一项任务" {
		t.Fatal("stale continue click changed the draft or sent a turn", err)
	}
	a.setDraft(" ")
	a.addAttachment("input.go")
	checkComposerAction(t, a.composerView(), "send")
	a.removeAttachment("0")
	checkComposerAction(t, a.composerView(), "continue")
	client.messages <- envelope{ID: json.RawMessage(`3`), Error: &rpcError{Message: "rejected"}}
	if err := a.handleCommand(click); err == nil {
		t.Fatal("expected rejected continuation")
	}
	checkComposerAction(t, a.composerView(), "continue")
	output.Reset()
	client.messages <- envelope{ID: json.RawMessage(`4`), Result: json.RawMessage(`{"turn":{"id":"next","status":"inProgress"}}`)}
	if err := a.handleCommand(click); err != nil {
		t.Fatal(err)
	}
	checkComposerAction(t, a.composerView(), "stop")
	var request struct {
		Method string
		Params struct {
			ThreadID string
			Input    json.RawMessage
		}
	}
	if err := json.Unmarshal(output.Bytes(), &request); err != nil || request.Method != "turn/start" || request.Params.ThreadID != c.ID ||
		string(request.Params.Input) != "[]" {
		t.Fatal("invalid continuation request", output.String(), err)
	}
	output.Reset()
	if err := a.handleCommand(click); err != nil || output.Len() != 0 {
		t.Fatal("repeated click sent an additional turn", err)
	}
}

func TestRunningComposerSendsFollowups(t *testing.T) {
	for _, event := range []peb.EditorEvent{{Kind: "submit", Ref: "form"}, {Kind: "click", Ref: "send"}} {
		t.Run(event.Kind, func(t *testing.T) {
			var output bytes.Buffer
			progress := &turnProgress{id: "turn", started: time.Now()}
			files := newTurnFiles("turn", t.TempDir())
			c := &conversation{thread: thread{ID: "thread", Name: "task"}, turnID: "turn", busy: true, loaded: true, progress: progress, files: files}
			a := &agent{selected: c, conversations: map[string]*conversation{c.ID: c}}
			a.client = &rpcClient{ctx: t.Context(), writer: json.NewEncoder(&output), messages: make(chan envelope, 2), handle: a.onMessage}
			if err := a.pasteImage(peb.EditorEvent{InputType: "image/png", Data: testImageURL(t), Value: "补充要求"}); err != nil {
				t.Fatal(err)
			}
			a.addAttachment("context.go")
			view := a.composerView()
			validateBindings(t, view)
			checkComposerAction(t, view, "send")
			for attempt := range 2 {
				a.client.messages <- envelope{ID: json.RawMessage([]byte{'1' + byte(attempt)}), Result: json.RawMessage(`{"turnId":"turn"}`)}
				if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: event}}); err != nil {
					t.Fatal(err)
				}
				if c.draft != "" || !c.busy || c.turnID != "turn" || c.progress != progress || c.files != files || !c.dirty || len(a.images) != 0 || len(a.attachments) != 0 {
					t.Fatal("steering reset the turn or retained accepted input")
				}
				checkComposerAction(t, a.composerView(), "stop")
				c.dirty = false
				a.setDraft("再补充一条")
				if !c.dirty {
					t.Fatal("typing did not schedule the send button update")
				}
				checkComposerAction(t, a.composerView(), "send")
			}
			decoder := json.NewDecoder(&output)
			for attempt := range 2 {
				var request struct {
					Params map[string]json.RawMessage
					Method string
				}
				if err := decoder.Decode(&request); err != nil {
					t.Fatal(err)
				}
				var inputs []input
				if err := json.Unmarshal(request.Params["input"], &inputs); err != nil {
					t.Fatal(err)
				}
				if request.Method != "turn/steer" || len(request.Params) != 3 || string(request.Params["threadId"]) != `"thread"` || string(request.Params["expectedTurnId"]) != `"turn"` {
					t.Fatal("followup did not use the steer protocol", output.String())
				}
				if attempt == 0 && (len(inputs) != 2 || !strings.Contains(inputs[0].Text, "context.go") || inputs[1].Type != "image") || attempt == 1 && (len(inputs) != 1 || inputs[0].Text != "再补充一条") {
					t.Fatal("followup lost input or reused previous attachments", inputs)
				}
			}
		})
	}
}

func TestSteerCompletionRace(t *testing.T) {
	for _, result := range []string{"accepted", "rejected", "disconnected"} {
		t.Run(result, func(t *testing.T) {
			a, c, output := asyncQuestionFixture(t)
			p := c.question("")
			a.client.messages <- envelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`)}
			switch result {
			case "accepted":
				a.client.messages <- envelope{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"turnId":"turn"}`)}
			case "rejected":
				a.client.messages <- envelope{ID: json.RawMessage(`1`), Error: &rpcError{Code: -32600, Message: "No active turn"}}
				a.client.messages <- envelope{ID: json.RawMessage(`2`), Result: json.RawMessage(`{"turn":{"id":"next-turn","status":"inProgress"}}`)}
			case "disconnected":
				close(a.client.messages)
			}
			questionEvent(t, a, p.ref+"/0/select/0", "click", "")
			err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "click", Ref: p.ref + "/0/next"}}})
			if result == "disconnected" {
				if !errors.Is(err, io.EOF) || p.answered || c.question("") != p {
					t.Fatal("transport failure lost the answer or hid the error", err)
				}
			} else if err != nil || !p.answered || c.busy != (result == "rejected") {
				t.Fatal("completion race lost the answer or running state", err)
			}
			if strings.Count(output.String(), `"method":"turn/steer"`) != 1 || strings.Contains(output.String(), `"method":"turn/start"`) != (result == "rejected") || c.draft != "保留草稿" {
				t.Fatal("answer was duplicated or the composer draft changed", output.String())
			}
			validateBindings(t, a.composerView())
		})
	}
}

func TestFollowupThinkingBeforeAcknowledgement(t *testing.T) {
	ui := &capturedUI{}
	c := &conversation{thread: thread{ID: "thread", Name: "task"}, loaded: true, draft: "补充要求"}
	a := &agent{selected: c, conversations: map[string]*conversation{c.ID: c}, publisher: ui, workspaceOpen: true}
	a.client = &rpcClient{ctx: t.Context(), writer: json.NewEncoder(io.Discard), messages: make(chan envelope, 2), handle: a.onMessage}
	notify := func(method, params string) {
		a.onMessage(envelope{Method: method, Params: json.RawMessage(params)})
	}
	notify("turn/started", `{"threadId":"thread","turn":{"id":"turn","status":"inProgress"}}`)
	notify("item/completed", `{"threadId":"thread","item":{"id":"first","type":"agentMessage","text":"原回复"}}`)
	started := c.progress.started
	submit := command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "submit", Ref: "form"}}}
	checkThinking := func(view *peb.EditorView, after string) {
		t.Helper()
		mark := strings.Index(view.HTML, `aria-label="正在思考"`)
		if at := strings.Index(view.HTML, `data-ref="message/`+after+`"`); at < 0 || mark < at || strings.Count(view.HTML, `class="codex-turn-progress"`) != 1 {
			t.Fatal("expected a single thinking indicator below the latest message", view.HTML)
		}
	}
	a.client.messages <- envelope{Method: "item/completed", Params: json.RawMessage(`{"threadId":"thread","item":{"id":"extra","type":"userMessage","text":"补充要求"}}`)}
	a.client.messages <- envelope{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"turnId":"turn"}`)}
	if err := a.handleCommand(submit); err != nil {
		t.Fatal(err)
	}
	checkThinking(ui.ui.View, "first")
	if strings.Contains(ui.ui.View.HTML, `data-ref="message/extra"`) {
		t.Fatal("first paint waited for the server to echo the followup")
	}
	notify("item/agentMessage/delta", `{"threadId":"thread","itemId":"first","delta":"迟到的内容"}`)
	notify("item/started", `{"threadId":"thread","item":{"id":"next","type":"reasoning"}}`)
	notify("item/reasoning/textDelta", `{"threadId":"thread","itemId":"next","delta":""}`)
	checkThinking(a.composerView(), "extra")
	if a.progressVisible() {
		t.Fatal("timer running while waiting for a followup reply")
	}
	notify("item/reasoning/textDelta", `{"threadId":"thread","itemId":"next","delta":"新内容"}`)
	if view := a.composerView(); strings.Contains(view.HTML, "正在思考") || !strings.Contains(view.HTML, "已处理") || !a.progressVisible() || c.progress.started != started {
		t.Fatal("new output did not resume the original turn timer", view.HTML)
	}
	c.draft = "再补充一条"
	a.client.messages <- envelope{ID: json.RawMessage(`2`), Error: &rpcError{Message: "rejected"}}
	if err := a.handleCommand(submit); err == nil {
		t.Fatal("expected rejected followup")
	}
	checkThinking(ui.ui.View, "extra")
	if view := a.composerView(); strings.Contains(view.HTML, "正在思考") || c.draft != "再补充一条" {
		t.Fatal("rejected followup left a pending indicator or lost the draft", view.HTML)
	}
	a.client.messages <- envelope{ID: json.RawMessage(`3`), Result: json.RawMessage(`{"turnId":"turn"}`)}
	if err := a.handleCommand(submit); err != nil {
		t.Fatal(err)
	}
	checkThinking(a.composerView(), "extra")
	notify("turn/completed", `{"threadId":"thread","turn":{"id":"turn","status":"interrupted","durationMs":12000}}`)
	if view := a.composerView(); strings.Contains(view.HTML, "正在思考") || !strings.Contains(view.HTML, "用时 12s") {
		t.Fatal("interruption left the followup waiting", view.HTML)
	}
}

func TestRejectedSteerPreservesDraftAndAttachments(t *testing.T) {
	a, c, output := asyncQuestionFixture(t)
	c.Name = "task"
	a.publisher = &capturedUI{}
	a.addAttachment("context.go")
	if err := a.pasteImage(peb.EditorEvent{InputType: "image/png", Data: testImageURL(t), Value: c.draft}); err != nil {
		t.Fatal(err)
	}
	a.client.messages <- envelope{ID: json.RawMessage(`1`), Error: &rpcError{Code: -32600, Message: "Turn mismatch"}}
	a.processCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "submit", Ref: "form"}}})
	if c.draft != "保留草稿" || len(a.images[a.attachmentScope()]) != 1 || len(a.attachments[a.attachmentScope()]) != 1 || !c.busy || strings.Contains(output.String(), `"method":"turn/start"`) {
		t.Fatal("rejected followup lost input or started another turn")
	}
	if ui := a.publisher.(*capturedUI).ui; ui.Kind != peb.Notification || !strings.Contains(ui.View.HTML, "Turn mismatch") {
		t.Fatal("send failure was not shown as a notification", ui)
	}
	a.client.messages <- envelope{ID: json.RawMessage(`2`), Result: json.RawMessage(`{"turnId":"turn"}`)}
	questionEvent(t, a, "send", "click", "")
	if c.draft != "" || len(a.images) != 0 || len(a.attachments) != 0 {
		t.Fatal("retry did not recover after a rejected send")
	}
}
