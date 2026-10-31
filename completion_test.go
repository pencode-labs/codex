package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pencode-labs/pencode/peb"
)

func completionInput(value string, caret int) peb.EditorEvent {
	return peb.EditorEvent{Kind: "input", Ref: "prompt", Value: value, Anchor: uint32(caret), Focus: uint32(caret)}
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
	if a.completion.kind != 0 || a.draft() != "" || len(a.skills[a.attachmentScope()]) != 1 || a.selected != nil || a.client != nil {
		t.Fatal("skill selection must prepare a chip without sending it or inserting Markdown")
	}
	view = a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, `data-pencode-caret="0:`) || !strings.Contains(view.HTML, `data-ref="remove-skill/0"`) || !strings.Contains(view.HTML, ">Code Review</span>") || strings.Contains(view.HTML, "[$code-review]") {
		t.Fatal("skill chip or input caret missing", view.HTML)
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

func TestMentionCompletionInsertsAtCaretAndAttachesFile(t *testing.T) {
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
	if a.completion.count != 1 || a.completion.items[0].path != "src/中文 [notes].go" || len(a.completion.files) != 2 {
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
	if !strings.HasPrefix(a.draft(), `请检查 [@中文 \[notes\].go](src/`) || !strings.HasSuffix(a.draft(), " ，然后继续") || strings.Contains(a.draft(), "@src/") {
		t.Fatal("mention corrupted surrounding text", a.draft())
	}
	files := a.attachments[a.attachmentScope()]
	if len(files) != 1 || files[0] != filepath.Join(root, "src/中文 [notes].go") || !strings.Contains(attachmentPrompt(a.draft(), files), files[0]) {
		t.Fatal("mention was not attached as task context", files)
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
