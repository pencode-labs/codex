package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pencode-labs/pencode/peb"
)

func TestProjectSettingsSurviveRestart(t *testing.T) {
	root := t.TempDir()
	for i := range permissionOptions {
		a := &agent{}
		if err := a.setContext(peb.ActionContext{ProjectPath: root}); err != nil {
			t.Fatal(err)
		}
		if err := a.saveProjectSettings("saved-model", "high", i); err != nil {
			t.Fatal(err)
		}
		// A new agent has no in-memory state from the process that saved it.
		restarted := &agent{}
		if err := restarted.setContext(peb.ActionContext{ProjectPath: root}); err != nil {
			t.Fatal(err)
		}
		if restarted.model != "saved-model" || restarted.effort != "high" || restarted.permissionIndex != i {
			t.Fatalf("restart restored model=%q permission=%d, want %d", restarted.model, restarted.permissionIndex, i)
		}
		data, err := os.ReadFile(filepath.Join(root, ".pencode", "codex.json"))
		if err != nil {
			t.Fatal(err)
		}
		var saved projectSettings
		if err := json.Unmarshal(data, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Model != "saved-model" || saved.ReasoningEffort != "high" || "permission/"+saved.Permission != permissionOptions[i].ref {
			t.Fatalf("unexpected settings: %s", data)
		}
	}
	// Selecting the default must clear a previously persisted model override.
	a := &agent{context: peb.ActionContext{ProjectPath: root}}
	if err := a.saveProjectSettings("", "", 0); err != nil {
		t.Fatal(err)
	}
	restarted := &agent{}
	if err := restarted.setContext(a.context); err != nil || restarted.model != "" || restarted.effort != "" || restarted.permissionIndex != 0 {
		t.Fatalf("default was not restored: %q, %d, %v", restarted.model, restarted.permissionIndex, err)
	}
}

func TestProjectSettingsFollowWorkspaceAndConversationSelection(t *testing.T) {
	first, second, fresh := t.TempDir(), t.TempDir(), t.TempDir()
	a := &agent{}
	for _, setting := range []struct {
		root, model string
		permission  int
	}{{first, "first-model", 2}, {second, "second-model", 1}} {
		if err := a.setContext(peb.ActionContext{ProjectPath: setting.root}); err != nil {
			t.Fatal(err)
		}
		if err := a.saveProjectSettings(setting.model, "medium", setting.permission); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.setContext(peb.ActionContext{ProjectPath: fresh}); err != nil {
		t.Fatal(err)
	}
	if a.model != "" || a.effort != "" || a.permissionIndex != 0 {
		t.Fatal("new project inherited another project's settings")
	}
	if _, err := os.Stat(filepath.Join(fresh, ".pencode")); !os.IsNotExist(err) {
		t.Fatalf("opening a project created configuration: %v", err)
	}
	firstChat := &conversation{thread: thread{ID: "first"}, context: peb.ActionContext{ProjectPath: first}}
	secondChat := &conversation{thread: thread{ID: "second"}, context: peb.ActionContext{ProjectPath: second}}
	a.conversations = map[string]*conversation{firstChat.ID: firstChat, secondChat.ID: secondChat}
	a.client = &rpcClient{ctx: context.Background()}
	for _, want := range []*conversation{firstChat, secondChat, firstChat} {
		want.loaded = true
		if err := a.resume(want.ID); err != nil {
			t.Fatal(err)
		}
		model, permission := "first-model", 2
		if want == secondChat {
			model, permission = "second-model", 1
		}
		if a.selected != want || a.context.ProjectPath != want.context.ProjectPath || a.model != model || a.effort != "medium" || a.permissionIndex != permission {
			t.Fatalf("conversation %q restored model=%q permission=%d", want.ID, a.model, a.permissionIndex)
		}
	}
}

func TestInvalidProjectSettingsResetPreviousProject(t *testing.T) {
	for _, data := range []string{`{`, `{"model":"other","permission":"unknown"}`, `{"permission":2}`} {
		t.Run(data, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, ".pencode"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".pencode", "codex.json"), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			a := &agent{model: "previous-model", permissionIndex: 2}
			if err := a.handleCommand(command{action: true, context: peb.ActionContext{ProjectPath: root}}); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
			if !a.workspaceOpen || a.model != "" || a.effort != "" || a.permissionIndex != 0 {
				t.Fatal("failed load retained previous project's settings or hid the panel")
			}
		})
	}
}

func TestFailedSettingsSelectionPreservesActiveSettings(t *testing.T) {
	root := t.TempDir()
	// A directory at the destination forces the final atomic rename to fail.
	directory := filepath.Join(root, ".pencode")
	if err := os.MkdirAll(filepath.Join(directory, "codex.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &agent{context: peb.ActionContext{ProjectPath: root}, model: "previous-model", permissionIndex: 1,
		modelSettings: true, permissionSettings: true, models: []modelOption{{Model: "next-model"}}}
	for _, selection := range []struct {
		handle func(peb.EditorEvent) error
		ref    string
	}{{a.handleModelEvent, "model/next-model"}, {a.handleModelEvent, "model-default"}, {a.handlePermissionEvent, "permission/full"}} {
		if err := selection.handle(peb.EditorEvent{Kind: "click", Ref: selection.ref}); err == nil || !strings.Contains(err.Error(), "保存 Codex 项目配置失败") {
			t.Fatalf("missing save failure for %q: %v", selection.ref, err)
		}
		if a.model != "previous-model" || a.permissionIndex != 1 {
			t.Fatal("failed save changed the active selection")
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "codex.json" {
		t.Fatalf("failed save left temporary files: %v, %v", entries, err)
	}
}

func TestProjectContextWarmPath(t *testing.T) {
	a := &agent{}
	ctx := peb.ActionContext{ProjectPath: t.TempDir()}
	if err := a.setContext(ctx); err != nil {
		t.Fatal(err)
	}
	if allocations := testing.AllocsPerRun(100, func() {
		if err := a.setContext(ctx); err != nil {
			panic(err)
		}
	}); allocations != 0 {
		t.Fatalf("same-project context allocates: %g", allocations)
	}
}
