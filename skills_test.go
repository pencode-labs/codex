package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pencode-labs/pencode/peb"
)

func TestSkillCatalogLoadsEnabledSkillsAndCaches(t *testing.T) {
	fixtureExecutable(t)
	a := &agent{ctx: t.Context(), context: peb.ActionContext{ProjectPath: t.TempDir()}}
	defer a.closeClient()
	a.updateCompletion(completionInput("/", 1))
	if !a.completion.skillsLoaded || a.completion.count != 2 || a.completion.skillError != "" {
		t.Fatal("skill catalog did not load", a.completion)
	}
	for _, skill := range a.completion.skills {
		if !skill.Enabled || !strings.HasPrefix(skill.Path, a.context.ProjectPath) {
			t.Fatal("catalog included disabled or unrelated skills", skill)
		}
	}
	if a.completion.items[0].detail != "审查代码" {
		t.Fatal("skill interface description was ignored")
	}
	client := a.client
	client.close()
	a.updateCompletion(completionInput("/审查", 3))
	if a.client != client || a.completion.count != 1 || a.completion.items[0].label != "fixture-review" {
		t.Fatal("cached filtering connected again or ignored metadata")
	}
	a.onMessage(envelope{Method: "skills/changed"})
	if a.completion.skillsLoaded || a.completion.kind != 0 {
		t.Fatal("changed catalog retained stale candidates")
	}
	a.updateCompletion(completionInput("/", 1))
	if !a.completion.skillsLoaded || a.client == client || a.completion.count != 2 {
		t.Fatal("catalog was not reloaded after invalidation")
	}
	if err := a.setContext(peb.ActionContext{ProjectPath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if a.completion.skillsLoaded || len(a.completion.skills) != 0 {
		t.Fatal("project switch retained another project's catalog")
	}
}

func TestSkillCatalogErrorCanRetry(t *testing.T) {
	fixtureExecutable(t)
	t.Setenv("PENCODE_CODEX_SKILLS_ERROR", "1")
	a := &agent{ctx: t.Context(), context: peb.ActionContext{ProjectPath: t.TempDir()}}
	defer a.closeClient()
	a.updateCompletion(completionInput("/", 1))
	if a.completion.skillsLoaded || a.completion.count != 0 || !strings.Contains(a.completion.skillError, "skill catalog failed") {
		t.Fatal("catalog error was hidden", a.completion)
	}
	view := a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, "skill catalog failed") {
		t.Fatal("catalog error did not reach the menu")
	}
	client := a.client
	client.close()
	a.updateCompletion(completionInput("/f", 2))
	if a.client != client {
		t.Fatal("failed catalog retried on every character")
	}
	t.Setenv("PENCODE_CODEX_SKILLS_ERROR", "")
	a.dismissCompletion()
	a.updateCompletion(completionInput("/", 1))
	if !a.completion.skillsLoaded || a.completion.skillError != "" || a.completion.count != 2 {
		t.Fatal("reopening did not retry failed catalog", a.completion)
	}
}

func TestSkillReferencesBecomeExplicitInputs(t *testing.T) {
	skill := skillOption{Name: "code-review", Path: filepath.Join(t.TempDir(), "skill space", "SKILL.md")}
	skill.prepare()
	prompt := skill.reference + " 检查代码 " + skill.reference
	var storage [3]input
	items := appendSkillInputs(append(storage[:0], input{Type: "text", Text: prompt}), prompt)
	if len(items) != 2 || items[1].Type != "skill" || items[1].Name != skill.Name || items[1].Path != skill.Path {
		t.Fatal("skill path was lost or duplicated", items)
	}
	encoded, err := json.Marshal(items[1])
	if err != nil || strings.Contains(string(encoded), `"text"`) || strings.Contains(string(encoded), "text_elements") {
		t.Fatal("skill input included text-only fields", string(encoded), err)
	}
	for _, invalid := range []string{"ordinary text", "$code-review", "[$bad name](/tmp/SKILL.md)", "[$bad](relative/SKILL.md)", "[$bad](/tmp/file.txt)", "[$bad](%zz)", "[$incomplete", "[$](path)", "[$bad](unterminated"} {
		if got := appendSkillInputs(storage[:1], invalid); len(got) != 1 {
			t.Fatalf("invalid reference became a skill input: %s", invalid)
		}
	}
	// References remain usable after catalog and conversation state is released.
	a := &agent{drafts: map[string]string{"": prompt}}
	a.completion = composerCompletion{}
	if got := appendSkillInputs(storage[:1], a.draft()); len(got) != 2 || got[1].Path != skill.Path {
		t.Fatal("restored draft lost its skill reference")
	}
	plain := "ordinary task without skills"
	if n := testing.AllocsPerRun(100, func() { appendSkillInputs(storage[:1], plain) }); n != 0 {
		t.Fatalf("ordinary task input allocated %g", n)
	}
}

func TestSelectedSkillsFollowDraftsAndKeepTextEditable(t *testing.T) {
	a := &agent{context: peb.ActionContext{ProjectPath: t.TempDir()}}
	skill := skillOption{Name: "agent-browser", Path: filepath.Join(a.context.ProjectPath, "skill space", "SKILL.md")}
	skill.Interface.DisplayName = "Agent Browser <UI>"
	skill.prepare()
	a.addSkill(&skill)
	a.addSkill(&skill)
	key := a.attachmentScope()
	if len(a.skills[key]) != 1 {
		t.Fatal("duplicate skill selection")
	}
	a.completion = composerCompletion{} // Catalog invalidation must not lose chips.
	value := "检查中文代码"
	if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: completionInput(value, utf8.RuneCountInString(value))}}); err != nil {
		t.Fatal(err)
	}
	view := a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, ">Agent Browser &lt;UI&gt;</span>") || strings.Contains(view.HTML, "<UI>") || !strings.Contains(view.HTML, ">"+value+"</textarea>") || strings.Contains(view.HTML, "[$agent-browser]") {
		t.Fatal("skill chip exposed Markdown, lost text, or failed to escape its label", view.HTML)
	}
	var storage [2]input
	inputs := appendSkillInputs(storage[:0], skillPrompt(a.draft(), a.skills[key]))
	if len(inputs) != 1 || inputs[0].Name != skill.Name || inputs[0].Path != skill.Path {
		t.Fatal("chip lost its explicit skill input", inputs)
	}
	if n := testing.AllocsPerRun(100, func() {
		a.addSkill(&skill)
		a.setDraft(value)
		a.updateCompletion(completionInput(value, utf8.RuneCountInString(value)))
		skillPrompt(value, nil)
	}); n != 0 {
		t.Fatalf("unchanged selection and ordinary input allocated %g", n)
	}
	a.selectConversation(&conversation{thread: thread{ID: "saved"}})
	if strings.Contains(a.composerView().HTML, `data-ref="remove-skill/0"`) {
		t.Fatal("new draft skill leaked into a conversation")
	}
	a.selectConversation(nil)
	root := a.context.ProjectPath
	a.context.ProjectPath = t.TempDir()
	if strings.Contains(a.composerView().HTML, `data-ref="remove-skill/0"`) {
		t.Fatal("skill leaked into another project")
	}
	a.context.ProjectPath = root
	if !strings.Contains(a.composerView().HTML, `data-ref="remove-skill/0"`) {
		t.Fatal("returning to a draft lost its skill")
	}
	for _, index := range []string{"-1", "1", "invalid"} {
		a.removeSkill(index)
	}
	if len(a.skills[key]) != 1 {
		t.Fatal("invalid removal changed selected skills")
	}
	if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "click", Ref: "remove-skill/0"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.skills[key]; ok || a.draft() != value || strings.Contains(a.composerView().HTML, `class="codex-skills"`) {
		t.Fatal("removing the chip changed text or retained its skill")
	}
}
