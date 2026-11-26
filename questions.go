package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/onix-engine/onix/html/markup"
	"github.com/pencode-labs/pencode/peb"
)

type userInputOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type userInputQuestion struct {
	ID       string `json:"id"`
	Header   string `json:"header"`
	Question string `json:"question"`
	draft    string
	Options  []userInputOption `json:"options"`
	selected int
	IsOther  bool `json:"isOther"`
	IsSecret bool `json:"isSecret"`
}

type userInputRequest struct {
	ref       string
	Questions []userInputQuestion `json:"questions"`
	current   int
	answered  bool
}

// ask/request_user_input_async emits an agentMessage, not a JSON-RPC request.
type asyncQuestion struct {
	Title   string   `json:"title"`
	Options []string `json:"options"`
}

func newAsyncQuestions(id string, questions []asyncQuestion) *userInputRequest {
	p := &userInputRequest{ref: previewID("question", id), Questions: make([]userInputQuestion, len(questions))}
	for index, question := range questions {
		q := &p.Questions[index]
		q.Question, q.IsOther, q.selected = question.Title, true, -1
		q.Options = make([]userInputOption, len(question.Options))
		for index, label := range question.Options {
			q.Options[index].Label = label
		}
	}
	return p
}

func (c *conversation) question(ref string) *userInputRequest {
	for _, r := range c.requests {
		if q := r.questions; q != nil && (ref == "" || q.ref == ref) {
			return q
		}
	}
	for _, item := range c.items {
		if q := item.questions; q != nil && !q.answered && (ref == "" || q.ref == ref) {
			return q
		}
	}
	return nil
}

// Async answers are normal user messages. History before a later user message
// must not reopen old questions when a conversation is resumed or paged.
func (c *conversation) resolvePastQuestions() {
	answered := false
	for index := len(c.items) - 1; index >= 0; index-- {
		item := c.items[index]
		if item.data.Type == "userMessage" {
			answered = true
		} else if answered && item.questions != nil && !item.questions.answered {
			item.questions.answered = true
			item.html = ""
		}
	}
}

func (a *agent) answerQuestion(c *conversation, p *userInputRequest, decision string) error {
	for _, r := range c.requests {
		if r.questions == p {
			return a.answer(c, string(r.id), decision)
		}
	}
	if decision != "skip" {
		if c.observing {
			return fmt.Errorf("任务由其他客户端执行，请在原客户端回答")
		}
		var b strings.Builder
		for index := range p.Questions {
			q := &p.Questions[index]
			if q.value() == "" {
				return fmt.Errorf("请回答所有问题")
			}
			if index > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(q.Question)
			b.WriteByte('\n')
			b.WriteString(q.value())
		}
		if err := a.connect(); err != nil {
			return err
		}
		if !c.loaded {
			if err := a.resume(c.ID); err != nil {
				return err
			}
			if !c.loaded {
				return fmt.Errorf("%s", c.status)
			}
		}
		inputs := []input{{Type: "text", Text: b.String()}}
		if err := a.sendTurn(c, inputs); err != nil {
			return err
		}
	}
	p.answered, c.dirty = true, true
	for _, item := range c.items {
		if item.questions == p {
			item.html = ""
			break
		}
	}
	return nil
}

type userInputAnswer struct {
	Answers []string `json:"answers"`
}

// The built-in question tool is disabled in Default mode unless the host opts in.
type questionConfig struct {
	RequestUserInput bool `json:"features.default_mode_request_user_input"`
}

func (q *userInputQuestion) allowsText() bool { return q.IsOther || len(q.Options) == 0 }

func (q *userInputQuestion) value() string {
	if q.selected >= 0 && q.selected < len(q.Options) {
		return q.Options[q.selected].Label
	}
	return strings.TrimSpace(q.draft)
}

// Parse once on receipt; editing and selecting answers do not allocate or decode JSON.
func (a *agent) handleQuestionEvent(e peb.EditorEvent) (bool, error) {
	if !strings.HasPrefix(e.Ref, "question-") {
		return false, nil
	}
	c := a.selected
	if c == nil {
		return true, nil
	}
	ref, rest, _ := strings.Cut(e.Ref, "/")
	index, action, _ := strings.Cut(rest, "/")
	n, err := strconv.Atoi(index)
	if err != nil {
		return true, nil
	}
	if p := c.question(ref); p != nil && p.current == n {
		q := &p.Questions[n]
		switch {
		case e.Kind == "input" && action == "input" && q.allowsText():
			if q.draft != e.Value || q.selected != -1 {
				q.draft, q.selected = e.Value, -1
				c.dirty = true
			}
		case e.Kind == "click" && strings.HasPrefix(action, "select/"):
			option, err := strconv.Atoi(strings.TrimPrefix(action, "select/"))
			if err == nil && option >= 0 && option < len(q.Options) && q.selected != option {
				q.selected, q.draft = option, ""
				c.dirty = true
			}
		case e.Kind == "click" && (action == "skip" || action == "close"):
			return true, a.answerQuestion(c, p, "skip")
		case e.Kind == "click" && action == "back" && n > 0:
			p.current--
			c.dirty = true
		case e.Kind == "click" && action == "next" || e.Kind == "submit" && action == "form":
			if q.value() == "" {
				return true, nil
			}
			if n+1 < len(p.Questions) {
				p.current++
				c.dirty = true
			} else {
				return true, a.answerQuestion(c, p, "answer")
			}
		}
		return true, nil
	}
	return true, nil
}

func (p *userInputRequest) render(b *markup.Builder, bindings []peb.EditorBinding) []peb.EditorBinding {
	q := &p.Questions[p.current]
	ref := p.ref + "/" + strconv.Itoa(p.current) + "/"
	b.Open("form", "class", "codex-question-card", "data-ref", ref+"form", "aria-label", "问题")
	b.Open("header", "class", "codex-question-header")
	b.TextElement("span", "?", "class", "codex-question-symbol", "aria-hidden", "true")
	b.TextElement("span", "问题")
	if q.Header != "" {
		b.TextElement("span", q.Header, "class", "codex-question-topic")
	}
	if len(p.Questions) > 1 {
		b.TextElement("span", strconv.Itoa(p.current+1)+" / "+strconv.Itoa(len(p.Questions)), "class", "codex-question-count")
	}
	bindings = questionButton(b, bindings, ref+"close", "codex-icon-button codex-question-close", "关闭问题并跳过", "×", false)
	b.End()
	b.Open("div", "class", "codex-question-body")
	b.TextElement("h3", q.Question)
	if len(q.Options) > 0 {
		b.Open("div", "class", "codex-question-options", "role", "group", "aria-label", "回答选项")
		for index, option := range q.Options {
			optionRef := ref + "select/" + strconv.Itoa(index)
			b.Open("button", "type", "button", "class", "codex-question-option", "data-ref", optionRef, "aria-pressed", strconv.FormatBool(q.selected == index))
			b.TextElement("span", strconv.Itoa(index+1), "class", "codex-question-number")
			b.Open("span", "class", "codex-question-option-text")
			b.TextElement("span", option.Label)
			if option.Description != "" {
				b.TextElement("span", option.Description, "class", "codex-question-description")
			}
			b.End()
			b.End()
			bindings = append(bindings, peb.EditorBinding{Ref: optionRef, Kind: "click"})
		}
		b.End()
	}
	b.End()
	b.Open("footer", "class", "codex-question-footer")
	if q.allowsText() {
		placeholder := "或自行撰写回复"
		if len(q.Options) == 0 {
			placeholder = "填写你的回答"
		}
		b.Open("label", "class", "codex-question-custom")
		b.Element("img", "src", "resources/icons/pencil.svg", "alt", "")
		field := b.Element("input", "data-ref", ref+"input", "aria-label", q.Question, "placeholder", placeholder, "value", q.draft)
		if q.IsSecret {
			field.SetAttr("type", "password")
			field.SetAttr("autocomplete", "off")
		}
		b.End()
		bindings = append(bindings, peb.EditorBinding{Ref: ref + "input", Kind: "input"})
	} else {
		b.Element("span", "class", "codex-input-spacer")
	}
	b.Open("div", "class", "codex-question-actions")
	if p.current > 0 {
		bindings = questionButton(b, bindings, ref+"back", "codex-question-back", "上一题", "上一题", false)
	}
	bindings = questionButton(b, bindings, ref+"skip", "codex-question-skip", "跳过问题", "跳过", false)
	label := "发送"
	if p.current+1 < len(p.Questions) {
		label = "下一题"
	}
	bindings = questionButton(b, bindings, ref+"next", "codex-question-send", label, label, q.value() == "")
	b.End()
	b.End()
	b.End()
	return append(bindings, peb.EditorBinding{Ref: ref + "form", Kind: "submit"})
}

func questionButton(b *markup.Builder, bindings []peb.EditorBinding, ref, class, title, label string, disabled bool) []peb.EditorBinding {
	button := b.TextElement("button", label, "type", "button", "data-ref", ref, "class", class, "title", title, "aria-label", title)
	if disabled {
		button.SetAttr("disabled", "")
	}
	return append(bindings, peb.EditorBinding{Ref: ref, Kind: "click"})
}
