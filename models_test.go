package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pencode-labs/pencode/peb"
)

func TestReasoningSelectionCancellationAndValidation(t *testing.T) {
	a := &agent{
		context: peb.ActionContext{ProjectPath: t.TempDir()},
		model:   "current", effort: "high", pendingModel: "next", modelSettings: true,
		models: []modelOption{{Model: "next", DisplayName: `<Next>`, DefaultReasoningEffort: "low",
			SupportedReasoningEfforts: []reasoningEffortOption{{"low"}, {"future-effort"}}}},
	}
	view := a.modelsView()
	validateBindings(t, view)
	if strings.Contains(view.HTML, "<Next>") || !strings.Contains(view.HTML, "&lt;Next&gt;") ||
		strings.Contains(view.HTML, `data-ref="effort/high"`) || !strings.Contains(view.HTML, `data-ref="effort/low" aria-checked="true"`) ||
		!strings.Contains(view.HTML, ">future-effort</span>") {
		t.Fatal("efforts did not follow the selected model catalog", view.HTML)
	}
	for _, ref := range []string{"effort/high", "effort/", "model-default"} {
		if err := a.handleModelEvent(peb.EditorEvent{Kind: "click", Ref: ref}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.handleModelEvent(peb.EditorEvent{Kind: "close"}); err != nil {
		t.Fatal(err)
	}
	if a.model != "current" || a.effort != "high" || a.pendingModel != "" || a.modelSettings {
		t.Fatal("cancellation changed active settings or retained pending state")
	}
	if _, err := os.Stat(filepath.Join(a.context.ProjectPath, ".pencode")); !os.IsNotExist(err) {
		t.Fatalf("unconfirmed selection wrote settings: %v", err)
	}
}

func TestReasoningSelectionSaveFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".pencode", "codex.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &agent{context: peb.ActionContext{ProjectPath: root}, model: "current", effort: "high",
		pendingModel: "next", modelSettings: true,
		models: []modelOption{{Model: "next", SupportedReasoningEfforts: []reasoningEffortOption{{"low"}}}},
	}
	if err := a.handleModelEvent(peb.EditorEvent{Kind: "click", Ref: "effort/low"}); err == nil {
		t.Fatal("missing save error")
	}
	if a.model != "current" || a.effort != "high" || !a.modelSettings || a.pendingModel != "next" {
		t.Fatal("failed save changed selection or closed the effort menu")
	}
}

func TestTurnEffortDefaults(t *testing.T) {
	a := &agent{models: []modelOption{
		{Model: "default", IsDefault: true, DefaultReasoningEffort: "medium"},
		{Model: "other", DefaultReasoningEffort: "low"},
	}}
	for _, selection := range []struct{ model, effort, want string }{
		{"other", "high", "high"}, {"other", "", "low"}, {"", "", "medium"}, {"unknown", "", ""},
	} {
		a.model, a.effort = selection.model, selection.effort
		if got := a.turnEffort(); got != selection.want {
			t.Fatalf("model=%q effort=%q: got %q, want %q", a.model, a.effort, got, selection.want)
		}
	}
	if n := testing.AllocsPerRun(100, func() { a.turnEffort() }); n != 0 {
		t.Fatalf("effort lookup allocated: %g", n)
	}
}
