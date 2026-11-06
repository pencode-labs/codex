package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pencode-labs/pencode/peb"
)

func questionFixture(t *testing.T, id, questions string) (*agent, *conversation, *bytes.Buffer) {
	t.Helper()
	output := new(bytes.Buffer)
	c := &conversation{thread: thread{ID: "thread"}, draft: "保留草稿", busy: true}
	a := &agent{client: &rpcClient{writer: json.NewEncoder(output)}, selected: c, conversations: map[string]*conversation{c.ID: c}}
	a.onMessage(envelope{ID: json.RawMessage(id), Method: "item/tool/requestUserInput", Params: json.RawMessage(`{"threadId":"thread","questions":` + questions + `}`)})
	return a, c, output
}

func questionEvent(t *testing.T, a *agent, ref, kind, value string) {
	t.Helper()
	if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: kind, Ref: ref, Value: value}}}); err != nil {
		t.Fatal(err)
	}
}

func TestQuestionCardSelectionAndCustomReply(t *testing.T) {
	a, c, output := questionFixture(t, `"ask/\"1"`, `[{"id":"goal/<id>","header":"工作方向","question":"你现在最希望我帮你完成哪项工作？","isOther":true,"options":[{"label":"开发新功能","description":"实现 <新需求>"},{"label":"排查并修复问题","description":"定位故障"},{"label":"审查代码"}]}]`)
	p := c.requests[0].questions
	ref := p.ref + "/0/"
	view := a.composerView()
	validateBindings(t, view)
	card := strings.Index(view.HTML, `class="codex-question-card"`)
	composer := strings.Index(view.HTML, `<section class="codex-composer">`)
	prompt := strings.Index(view.HTML, `class="codex-input-card"`)
	if card < composer || card > prompt || strings.Contains(view.HTML, "Codex 需要你的确认") {
		t.Fatal("question must appear above the composer input", view.HTML)
	}
	for _, want := range []string{"实现 &lt;新需求&gt;", "或自行撰写回复", `aria-pressed="false"`, `aria-label="发送" disabled`} {
		if !strings.Contains(view.HTML, want) {
			t.Fatalf("missing %q: %s", want, view.HTML)
		}
	}
	questionEvent(t, a, ref+"next", "click", "")
	if output.Len() != 0 {
		t.Fatal("empty answer was submitted")
	}
	questionEvent(t, a, ref+"select/1", "click", "")
	view = a.composerView()
	validateBindings(t, view)
	if output.Len() != 0 || !c.dirty || p.Questions[0].value() != "排查并修复问题" || !strings.Contains(view.HTML, `aria-pressed="true"`) || strings.Contains(view.HTML, `aria-label="发送" disabled`) {
		t.Fatal("selection did not update the card without sending")
	}
	questionEvent(t, a, ref+"input", "input", " 自定义 <回答> ")
	view = a.composerView()
	if p.Questions[0].selected != -1 || strings.Contains(view.HTML, `aria-pressed="true"`) || !strings.Contains(view.HTML, "自定义 &lt;回答&gt;") {
		t.Fatal("custom answer did not replace the selection", view.HTML)
	}
	questionEvent(t, a, ref+"form", "submit", "")
	var reply struct {
		Result struct {
			Answers map[string]userInputAnswer `json:"answers"`
		} `json:"result"`
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(output.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if string(reply.ID) != `"ask/\"1"` || len(reply.Result.Answers["goal/<id>"].Answers) != 1 || reply.Result.Answers["goal/<id>"].Answers[0] != "自定义 <回答>" || len(c.requests) != 0 || c.draft != "保留草稿" || !c.busy {
		t.Fatal("reply lost the wire ID, answer, draft or running turn", output.String())
	}
	questionEvent(t, a, ref+"next", "click", "")
	if bytes.Count(output.Bytes(), []byte(`"result"`)) != 1 {
		t.Fatal("stale question sent a second reply")
	}
}

func TestQuestionNavigationRetainsAnswersAndMasksSecrets(t *testing.T) {
	a, c, output := questionFixture(t, `9`, `[{"id":"choice","question":"选择方向","options":[{"label":"A"},{"label":"B"}]},{"id":"token","question":"输入密钥","isSecret":true,"options":null}]`)
	p := c.requests[0].questions
	first, second := p.ref+"/0/", p.ref+"/1/"
	if strings.Contains(a.composerView().HTML, first+"input") {
		t.Fatal("fixed-choice question exposed custom input")
	}
	questionEvent(t, a, first+"select/0", "click", "")
	questionEvent(t, a, first+"form", "submit", "")
	view := a.composerView()
	validateBindings(t, view)
	if p.current != 1 || output.Len() != 0 || !strings.Contains(view.HTML, "2 / 2") || !strings.Contains(view.HTML, `type="password" autocomplete="off"`) {
		t.Fatal("next question lost the secret input or submitted early", view.HTML)
	}
	questionEvent(t, a, first+"select/1", "click", "")
	if p.Questions[0].value() != "A" {
		t.Fatal("stale option changed a previous question")
	}
	questionEvent(t, a, second+"input", "input", "secret")
	questionEvent(t, a, second+"back", "click", "")
	questionEvent(t, a, first+"select/1", "click", "")
	questionEvent(t, a, first+"next", "click", "")
	if p.Questions[1].value() != "secret" {
		t.Fatal("navigation lost a custom answer")
	}
	questionEvent(t, a, second+"next", "click", "")
	if !strings.Contains(output.String(), `"choice":{"answers":["B"]}`) || !strings.Contains(output.String(), `"token":{"answers":["secret"]}`) || len(c.requests) != 0 {
		t.Fatal(output.String())
	}
}

func TestQuestionSkipCloseAndRemoteResolution(t *testing.T) {
	for _, action := range []string{"skip", "close", "resolved", "completed"} {
		t.Run(action, func(t *testing.T) {
			a, c, output := questionFixture(t, `"pending"`, `[{"id":"q","question":"问题","options":null}]`)
			ref := c.requests[0].questions.ref + "/0/"
			questionEvent(t, a, ref+"input", "input", "unfinished answer")
			switch action {
			case "resolved":
				a.onMessage(envelope{Method: "serverRequest/resolved", Params: json.RawMessage(`{"threadId":"thread","requestId":"pending"}`)})
			case "completed":
				a.onMessage(envelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"interrupted"}}`)})
			default:
				questionEvent(t, a, ref+action, "click", "")
				if !strings.Contains(output.String(), `"result":{"answers":{}}`) {
					t.Fatal("skip must resolve the request with no answers", output.String())
				}
			}
			written := output.Len()
			questionEvent(t, a, ref+"form", "submit", "")
			view := a.composerView()
			validateBindings(t, view)
			if len(c.requests) != 0 || strings.Contains(view.HTML, `class="codex-question-card"`) || written != output.Len() || c.draft != "保留草稿" {
				t.Fatal("resolved question remained active or lost the draft")
			}
			if (action == "resolved" || action == "completed") && written != 0 {
				t.Fatal("server cleanup sent an unsolicited reply")
			}
		})
	}
}

func TestQuestionEditingDoesNotAllocate(t *testing.T) {
	a, c, _ := questionFixture(t, `1`, `[{"id":"q","question":"问题","isOther":true,"options":[{"label":"A"},{"label":"B"}]}]`)
	ref := c.requests[0].questions.ref + "/0/"
	input := peb.EditorEvent{Kind: "input", Ref: ref + "input", Value: "custom"}
	selectOption := peb.EditorEvent{Kind: "click", Ref: ref + "select/1"}
	if allocs := testing.AllocsPerRun(100, func() {
		_, _ = a.handleQuestionEvent(input)
		_, _ = a.handleQuestionEvent(selectOption)
	}); allocs != 0 {
		t.Fatalf("editing questions allocated %g", allocs)
	}
}

func TestMalformedQuestionRequestIsRejected(t *testing.T) {
	a, c, output := questionFixture(t, `1`, `[]`)
	if len(c.requests) != 0 || !strings.Contains(output.String(), `"code":-32602`) {
		t.Fatal("empty question request left the server waiting", output.String())
	}
	validateBindings(t, a.composerView())
}

func TestConcurrentQuestionsShowOneCardAtATime(t *testing.T) {
	a, c, _ := questionFixture(t, `1`, `[{"id":"q1","question":"第一个问题","options":null}]`)
	a.onMessage(envelope{ID: json.RawMessage(`2`), Method: "item/tool/requestUserInput", Params: json.RawMessage(`{"threadId":"thread","questions":[{"id":"q2","question":"第二个问题","options":null}]}`)})
	view := a.composerView()
	validateBindings(t, view)
	if len(c.requests) != 2 || strings.Count(view.HTML, `class="codex-question-card"`) != 1 || strings.Contains(view.HTML, "第二个问题") {
		t.Fatal("concurrent question cards overflowed the composer", view.HTML)
	}
	ref := c.requests[0].questions.ref + "/0/skip"
	questionEvent(t, a, ref, "click", "")
	view = a.composerView()
	validateBindings(t, view)
	if len(c.requests) != 1 || strings.Contains(view.HTML, "第一个问题") || !strings.Contains(view.HTML, "第二个问题") {
		t.Fatal("next pending question was lost", view.HTML)
	}
}
