package main

import (
	"strings"
	"testing"

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
