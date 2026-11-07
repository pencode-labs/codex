package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pencode-labs/pencode/peb"
)

// Captured from the real model's ask tool. This is a notification with no RPC id.
const asyncQuestionEvent = `{"threadId":"thread","item":{"id":"ask-call","type":"agentMessage","delivery":"async","phase":"final_answer","text":"你接下来想让我帮你做什么？\n- 检查现有代码\n- 实现一个功能","questions":[{"title":"你接下来想让我帮你做什么？","options":["检查现有代码","实现一个功能"]}]}}`

func asyncQuestionFixture(t *testing.T) (*agent, *conversation, *bytes.Buffer) {
	t.Helper()
	output := new(bytes.Buffer)
	c := &conversation{thread: thread{ID: "thread"}, draft: "保留草稿", turnID: "turn", busy: true, loaded: true}
	a := &agent{selected: c, conversations: map[string]*conversation{c.ID: c}}
	a.client = &rpcClient{ctx: t.Context(), writer: json.NewEncoder(output), messages: make(chan envelope, 4), handle: a.onMessage}
	a.onMessage(envelope{Method: "item/completed", Params: json.RawMessage(asyncQuestionEvent)})
	return a, c, output
}

func TestAsyncQuestionCardSurvivesTurnCompletion(t *testing.T) {
	a, c, output := asyncQuestionFixture(t)
	p := c.question("")
	if p == nil || len(c.requests) != 0 {
		t.Fatal("async questions must not depend on a pending RPC")
	}
	for _, method := range []string{"item/started", "item/completed", "turn/completed"} {
		params := asyncQuestionEvent
		if method == "turn/completed" {
			params = `{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`
		}
		a.onMessage(envelope{Method: method, Params: json.RawMessage(params)})
		view := a.composerView()
		validateBindings(t, view)
		card := strings.Index(view.HTML, `class="codex-question-card"`)
		if card < strings.Index(view.HTML, `<section class="codex-composer">`) || strings.Count(view.HTML, "你接下来想让我帮你做什么？") != 2 || c.question("") != p {
			t.Fatal("async question duplicated in transcript or lost its card", view.HTML)
		}
	}
	if c.busy || output.Len() != 0 {
		t.Fatal("async question must not keep the turn blocked or send an RPC reply")
	}
	questionEvent(t, a, p.ref+"/0/close", "click", "")
	if c.question("") != nil || !p.answered || output.Len() != 0 || c.draft != "保留草稿" {
		t.Fatal("dismissing an async question changed the draft or contacted the server")
	}
}

func TestAsyncQuestionAnswerUsesUserInput(t *testing.T) {
	for _, active := range []bool{true, false} {
		name := "start"
		if active {
			name = "steer"
		}
		t.Run(name, func(t *testing.T) {
			a, c, output := asyncQuestionFixture(t)
			c.busy = active
			p := c.question("")
			a.client.messages <- envelope{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"turnId":"turn","turn":{"id":"next-turn","status":"inProgress"}}`)}
			questionEvent(t, a, p.ref+"/0/select/1", "click", "")
			questionEvent(t, a, p.ref+"/0/next", "click", "")
			var request struct {
				Method string `json:"method"`
				Params struct {
					ThreadID       string  `json:"threadId"`
					ExpectedTurnID string  `json:"expectedTurnId"`
					Input          []input `json:"input"`
				} `json:"params"`
			}
			if err := json.Unmarshal(output.Bytes(), &request); err != nil {
				t.Fatal(err)
			}
			if request.Method != "turn/"+name || request.Params.ThreadID != c.ID || len(request.Params.Input) != 1 || request.Params.Input[0].Text != "你接下来想让我帮你做什么？\n实现一个功能" || active && request.Params.ExpectedTurnID != "turn" {
				t.Fatal("async answer was not sent as correlated user input", output.String())
			}
			if c.question("") != nil || !p.answered || !c.busy || c.draft != "保留草稿" {
				t.Fatal("answer did not dismiss the card, resume work or preserve draft")
			}
			written := output.Len()
			questionEvent(t, a, p.ref+"/0/next", "click", "")
			if output.Len() != written {
				t.Fatal("stale question submitted twice")
			}
		})
	}
}

func TestAsyncQuestionRetainsAnswerOnSendFailure(t *testing.T) {
	a, c, output := asyncQuestionFixture(t)
	p := c.question("")
	a.client.messages <- envelope{ID: json.RawMessage(`1`), Error: &rpcError{Code: -32603, Message: "send failed"}}
	questionEvent(t, a, p.ref+"/0/input", "input", "自定义回答")
	err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "click", Ref: p.ref + "/0/next"}}})
	if err == nil || c.question("") != p || p.Questions[0].value() != "自定义回答" || !strings.Contains(output.String(), "turn/steer") {
		t.Fatal("failed answer lost the pending question or draft", err)
	}
}

func TestAsyncAnswerHandlesCompletionWhileStartingNextTurn(t *testing.T) {
	for _, completed := range []string{"turn", "next-turn"} {
		t.Run(completed, func(t *testing.T) {
			a, c, _ := asyncQuestionFixture(t)
			c.busy = false
			p := c.question("")
			a.client.messages <- envelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"` + completed + `","status":"completed"}}`)}
			a.client.messages <- envelope{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"turn":{"id":"next-turn","status":"inProgress"}}`)}
			questionEvent(t, a, p.ref+"/0/select/0", "click", "")
			questionEvent(t, a, p.ref+"/0/next", "click", "")
			if c.busy != (completed == "turn") {
				t.Fatal("busy state used completion from the wrong turn", completed, c.busy)
			}
		})
	}
}

func TestAsyncQuestionsFromHistory(t *testing.T) {
	a, c, _ := asyncQuestionFixture(t)
	old := c.items[0]
	question := threadItem{ID: "later-question", Type: "agentMessage", Text: "第二个问题", Questions: []asyncQuestion{{Title: "第二个问题"}}}
	c.items = append(c.items, newChatItem(threadItem{ID: "answer", Type: "userMessage"}, true), newChatItem(question, true))
	c.resolvePastQuestions()
	if !old.questions.answered || c.question("") != c.items[2].questions {
		t.Fatal("history revived an answered question or hid an unanswered question")
	}
	view := a.composerView()
	if !strings.Contains(view.HTML, "第二个问题") {
		t.Fatal("unanswered persisted question did not render")
	}
	plain := newChatItem(threadItem{ID: "plain-text", Type: "agentMessage", Text: "问题？\n- A\n- B"}, true)
	if plain.questions != nil {
		t.Fatal("ordinary Markdown was interpreted as a tool question")
	}
}

func TestAsyncQuestionEditingDoesNotAllocate(t *testing.T) {
	a, c, _ := asyncQuestionFixture(t)
	ref := c.question("").ref + "/0/"
	edit := peb.EditorEvent{Kind: "input", Ref: ref + "input", Value: "custom"}
	choose := peb.EditorEvent{Kind: "click", Ref: ref + "select/1"}
	if allocs := testing.AllocsPerRun(100, func() {
		_, _ = a.handleQuestionEvent(edit)
		_, _ = a.handleQuestionEvent(choose)
	}); allocs != 0 {
		t.Fatalf("editing async questions allocated %g", allocs)
	}
}
