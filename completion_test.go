package main

import (
	"bytes"
	"encoding/json"
	"html"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pencode-labs/pencode/peb"
)

func completionInput(value string, caret int) peb.EditorEvent {
	return peb.EditorEvent{Kind: "input", Ref: "prompt", Value: value, Anchor: uint32(caret), Focus: uint32(caret)}
}

func TestMarkdownReferenceEscapesLabelsAndPaths(t *testing.T) {
	skill := skillOption{Name: `review [中文]`, Path: "/skills/space (dir)/SKILL.md"}
	skill.prepare()
	if want := `[review \[中文\]](/skills/space%20%28dir%29/SKILL.md)`; skill.reference != want {
		t.Fatalf("skill reference = %q, want %q", skill.reference, want)
	}
}

func TestCompletionToken(t *testing.T) {
	for _, test := range []struct {
		value, token string
		caret        int
		kind         byte
	}{
		{"/", "/", 1, '/'}, {" \n/model", "/model", 8, '/'},
		{"请检查 @中文.go 之后", "@中文.go", 10, '@'},
		{"@src/main.go tail", "@src/ma", 7, '@'},
		{"hello/", "", 6, 0}, {"a@b", "", 3, 0},
		{"https://example.com", "", 19, 0}, {"hello /new", "", 10, 0},
		{"", "", 0, 0}, {"@file ", "", 6, 0},
	} {
		t.Run(test.value, func(t *testing.T) {
			kind, start, end := completionToken(test.value, uint32(test.caret), uint32(test.caret))
			if kind != test.kind || kind != 0 && test.value[start:end] != test.token {
				t.Fatalf("token = %q %q; want %q %q", kind, test.value[start:end], test.kind, test.token)
			}
		})
	}
	if kind, _, _ := completionToken("@main.go", 1, 8); kind != 0 {
		t.Fatal("selected text opened completion")
	}
}

func TestSlashCompletionSkills(t *testing.T) {
	a := &agent{context: peb.ActionContext{ProjectPath: t.TempDir()}}
	for _, name := range []string{"code-review", "write-tests"} {
		skill := skillOption{Name: name, Path: filepath.Join(a.context.ProjectPath, ".agents", "skills", name, "SKILL.md"), Description: "项目技能"}
		skill.prepare()
		a.completion.skills = append(a.completion.skills, skill)
	}
	a.completion.skillsLoaded = true
	input := func(value string) {
		t.Helper()
		if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: completionInput(value, utf8.RuneCountInString(value))}}); err != nil {
			t.Fatal(err)
		}
	}
	key := func(data string) {
		t.Helper()
		event := completionInput(a.draft(), utf8.RuneCountInString(a.draft()))
		event.Kind, event.Data = "completion", data
		if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: event}}); err != nil {
			t.Fatal(err)
		}
	}
	input("/")
	if a.completion.count != 2 {
		t.Fatal("skills missing", a.completion.count)
	}
	view := a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, `data-pencode-completion="true"`) || !strings.Contains(view.HTML, `role="listbox"`) {
		t.Fatal("input is not connected to completion", view.HTML)
	}
	if strings.Count(view.HTML, `class="codex-completion-icon" src="resources/icons/layers-2.svg"`) != 2 || strings.Contains(view.HTML, `class="codex-completion-label">/`) {
		t.Fatal("skill suggestions must show icons instead of slash prefixes", view.HTML)
	}
	key("previous")
	if a.completion.selected != a.completion.count-1 {
		t.Fatal("previous did not wrap")
	}
	key("next")
	if a.completion.selected != 0 {
		t.Fatal("next did not wrap")
	}
	key("dismiss")
	input("/") // The host synchronizes the current draft before Enter.
	if a.draft() != "/" || a.completion.kind != 0 || strings.Contains(a.composerView().HTML, `role="listbox"`) {
		t.Fatal("dismiss changed draft or retained menu")
	}
	input("/code")
	if a.completion.count != 1 || a.completion.items[0].label != "code-review" {
		t.Fatal("skill filtering failed", a.completion)
	}
	key("accept")
	reference := a.completion.skills[0].reference + " "
	if !strings.HasPrefix(reference, "[code-review](") {
		t.Fatal("skill label retained a completion prefix", reference)
	}
	if a.completion.kind != 0 || a.draft() != reference || a.selected != nil || a.client != nil {
		t.Fatal("skill selection must insert its Markdown link without sending it", a.draft())
	}
	view = a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, `data-pencode-markdown-links`) || !strings.Contains(view.HTML, `data-pencode-caret="`+strconv.Itoa(utf8.RuneCountInString(reference))+`:`) || !strings.Contains(view.HTML, html.EscapeString(reference)+"</textarea>") {
		t.Fatal("skill Markdown or input caret missing", view.HTML)
	}
	input("/new")
	key("accept")
	if a.draft() != "/new" || a.completion.count != 0 {
		t.Fatal("built-in commands must not appear in the skill picker")
	}
	input("/does-not-exist")
	key("accept")
	if a.draft() != "/does-not-exist" || a.completion.count != 0 {
		t.Fatal("empty completion changed the draft")
	}
	input("ordinary text")
	if a.completion.kind != 0 {
		t.Fatal("ordinary text retained suggestions")
	}
}

func TestMentionCompletionInsertsLinkAtCaret(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"src/中文 [notes].go", "other/中文.go", "node_modules/中文.js", ".git/中文"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a := &agent{context: peb.ActionContext{ProjectPath: root}}
	value := "请检查 @src/中文，然后继续"
	// Search in the middle of a Chinese message and preserve the suffix.
	caret := utf8.RuneCountInString("请检查 @src/中文")
	a.setDraft(value)
	a.updateCompletion(completionInput(value, caret))
	if a.completion.count != 1 || a.completion.items[0].path != "src/中文 [notes].go" || len(a.completion.files) != 4 {
		t.Fatal("project filtering failed", a.completion)
	}
	view := a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, "中文 [notes].go") {
		t.Fatal(view.HTML)
	}
	if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "click", Ref: "completion/0"}}}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a.draft(), `请检查 [中文 \[notes\].go](src/`) || !strings.HasSuffix(a.draft(), " ，然后继续") || strings.Contains(a.draft(), "@src/") {
		t.Fatal("mention corrupted surrounding text", a.draft())
	}
	if len(a.attachments) != 0 {
		t.Fatal("mention must only insert its Markdown link")
	}
	if a.completion.caret != utf8.RuneCountInString(strings.TrimSuffix(a.draft(), "，然后继续")) {
		t.Fatal("caret did not follow inserted mention", a.completion.caret)
	}
	// A new project must not reuse the previous project's file index.
	if err := a.setContext(peb.ActionContext{ProjectPath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	a.updateCompletion(completionInput("@", 1))
	if a.completion.count != 0 || len(a.completion.files) != 0 {
		t.Fatal("project switch retained file candidates")
	}
}

func TestMentionCompletionIncludesEmptyDirectories(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"src/空 [目录]", "node_modules/hidden", ".git/hidden"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	a := &agent{context: peb.ActionContext{ProjectPath: root}}
	a.setDraft("查看 @目录 后续")
	a.updateCompletion(completionInput(a.draft(), utf8.RuneCountInString("查看 @目录")))
	if a.completion.count != 1 || a.completion.items[0].path != "src/空 [目录]/" || len(a.completion.files) != 2 {
		t.Fatal("directory search included ignored paths or missed an empty directory", a.completion)
	}
	if err := a.handleCompletion(peb.EditorEvent{Kind: "click", Data: "accept"}); err != nil {
		t.Fatal(err)
	}
	want := `查看 [空 \[目录\]/](src/%E7%A9%BA%20%5B%E7%9B%AE%E5%BD%95%5D/)  后续`
	if a.draft() != want || len(a.attachments) != 0 {
		t.Fatalf("directory mention = %q; want %q", a.draft(), want)
	}
}

func TestCompletionLinksSendVerbatimAndRetry(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "目录"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes [a].md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c := &conversation{thread: thread{ID: "saved", Name: "task"}, context: peb.ActionContext{ProjectPath: root}, loaded: true}
	a := &agent{context: c.context, selected: c}
	skill := skillOption{Name: "review", Path: filepath.Join(root, "skill space", "SKILL.md")}
	skill.prepare()
	a.completion.skills, a.completion.skillsLoaded = []skillOption{skill}, true
	for _, token := range []string{"/review", "@notes", "@目录"} {
		a.setDraft(a.draft() + token)
		e := completionInput(a.draft(), utf8.RuneCountInString(a.draft()))
		a.updateCompletion(e)
		if a.completion.count != 1 {
			t.Fatal("missing completion", token, a.completion)
		}
		e.Kind, e.Data = "completion", "accept"
		if err := a.handleCompletion(e); err != nil {
			t.Fatal(err)
		}
	}
	prompt := a.draft() + "\n检查这些内容，保留  空格。\n"
	a.setDraft(prompt)
	var output bytes.Buffer
	a.client = &rpcClient{ctx: t.Context(), writer: json.NewEncoder(&output), messages: make(chan envelope, 2)}
	a.client.messages <- envelope{ID: json.RawMessage(`1`), Error: &rpcError{Code: -32000, Message: "retry"}}
	a.client.messages <- envelope{ID: json.RawMessage(`2`), Result: json.RawMessage(`{"turn":{"id":"turn","status":"completed"}}`)}
	if err := a.send(); err == nil || a.draft() != prompt || c.busy {
		t.Fatal("failed send lost the Markdown draft", err)
	}
	if err := a.send(); err != nil || a.draft() != "" {
		t.Fatal("successful retry did not clear the draft", err)
	}
	decoder := json.NewDecoder(&output)
	for range 2 {
		var request struct {
			Method string
			Params struct{ Input []input }
		}
		if err := decoder.Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Method != "turn/start" || len(request.Params.Input) != 1 || request.Params.Input[0].Type != "text" || request.Params.Input[0].Text != prompt {
			t.Fatalf("Markdown was changed or augmented before sending: %+v", request)
		}
	}
}

func TestCompletionDismissRestoresCaret(t *testing.T) {
	for _, value := range []string{"/review", "请检查 @中文.go 后缀"} {
		t.Run(value, func(t *testing.T) {
			a := &agent{context: peb.ActionContext{ProjectPath: t.TempDir()}}
			a.completion.skillsLoaded = true
			caret := utf8.RuneCountInString(strings.TrimSuffix(value, " 后缀"))
			event := completionInput(value, caret)
			a.setDraft(value)
			a.updateCompletion(event)
			if a.completion.kind == 0 {
				t.Fatal("completion did not open")
			}
			event.Kind, event.Data = "completion", "dismiss"
			if err := a.handleCompletion(event); err != nil {
				t.Fatal(err)
			}
			view := a.composerView()
			validateBindings(t, view)
			if a.draft() != value || strings.Contains(view.HTML, `role="listbox"`) || !strings.Contains(view.HTML, `data-pencode-caret="`+strconv.Itoa(caret)+`:`) {
				t.Fatal("dismiss did not close suggestions and restore focus at the original caret", view.HTML)
			}
			a.updateCompletion(completionInput(value, caret))
			if a.completion.kind != 0 {
				t.Fatal("restoring focus reopened dismissed suggestions")
			}
		})
	}
}

func TestCompletionRejectsChangedSelectionAndEscapesNames(t *testing.T) {
	a := &agent{}
	a.setDraft("/review")
	a.updateCompletion(completionInput(a.draft(), 7))
	event := completionInput(a.draft(), 0)
	event.Kind, event.Data = "completion", "accept"
	if err := a.handleCompletion(event); err != nil || a.draft() != "/review" || a.completion.kind != 0 {
		t.Fatal("stale completion changed the draft", err)
	}
	a.completion.kind, a.completion.count = '@', 1
	a.completion.items[0] = composerCandidate{label: `<script>`, detail: `" onmouseover="bad`}
	view := a.composerView()
	validateBindings(t, view)
	if strings.Contains(view.HTML, "<script>") || !strings.Contains(view.HTML, "&lt;script&gt;") || !strings.Contains(view.HTML, "&#34;") {
		t.Fatal("completion names were not escaped", view.HTML)
	}
	a.selectConversation(&conversation{})
	if a.completion.kind != 0 {
		t.Fatal("conversation switch retained completion")
	}
}

func TestCompletionWarmInputDoesNotAllocate(t *testing.T) {
	a := &agent{drafts: map[string]string{"": ""}}
	plain := completionInput("ordinary input", 14)
	if n := testing.AllocsPerRun(100, func() { a.setDraft(plain.Value); a.updateCompletion(plain) }); n != 0 {
		t.Fatalf("ordinary input allocated %g", n)
	}
	a.completion.root, a.completion.files, a.completion.indexed = "", []completionFile{{"src/main.go", "src/main.go"}}, time.Now()
	a.updateCompletion(completionInput("@", 1))
	first, second := completionInput("@ma", 3), completionInput("@main", 5)
	if n := testing.AllocsPerRun(100, func() { a.updateCompletion(first); a.updateCompletion(second) }); n != 0 {
		t.Fatalf("cached file filtering allocated %g", n)
	}
	if a.completion.count != 1 {
		t.Fatal("allocation check did not filter cached files")
	}
	skill := skillOption{Name: "code-review", Description: "审查代码", Path: filepath.Join(t.TempDir(), "SKILL.md")}
	skill.prepare()
	a.completion.skills, a.completion.skillsLoaded = []skillOption{skill}, true
	first, second = completionInput("/co", 3), completionInput("/code", 5)
	if n := testing.AllocsPerRun(100, func() { a.updateCompletion(first); a.updateCompletion(second) }); n != 0 {
		t.Fatalf("skill filtering allocated %g", n)
	}
	if a.completion.count != 1 {
		t.Fatal("allocation check did not filter cached skills")
	}
}
