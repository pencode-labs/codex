package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/pencode-labs/pencode/peb"
)

func TestResolveExecutablePath(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	homeVariable := "HOME"
	if runtime.GOOS == "windows" {
		homeVariable = "USERPROFILE"
	}
	t.Setenv(homeVariable, home)
	t.Setenv("PENCODE_CODEX_PATH", "")
	absolute := filepath.Join(root, "Program Files", "Codex", "codex")
	for _, tc := range []struct{ name, path, want string }{
		{"default", "", "codex"},
		{"blank", "  ", "codex"},
		{"command", "codex-dev", "codex-dev"},
		{"absolute with spaces", absolute, absolute},
		{"forward slashes", filepath.ToSlash(absolute), absolute},
		{"relative", "./tools with spaces/codex", filepath.Join(root, "tools with spaces", "codex")},
		{"native relative", filepath.Join("tools", "codex"), filepath.Join(root, "tools", "codex")},
		{"home", "~/.local/bin/codex", filepath.Join(home, ".local", "bin", "codex")},
		{"native home", "~" + string(filepath.Separator) + "bin" + string(filepath.Separator) + "codex", filepath.Join(home, "bin", "codex")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveExecutablePath(root, tc.path)
			if err != nil || got != tc.want {
				t.Fatalf("resolve %q = %q, %v; want %q", tc.path, got, err, tc.want)
			}
		})
	}
	if runtime.GOOS == "windows" {
		for _, path := range []string{`C:\Tools\codex.exe`, `\\server\share\Codex CLI\codex.exe`} {
			if got, err := resolveExecutablePath(root, path); err != nil || got != path {
				t.Fatalf("Windows path changed: %q, %v", got, err)
			}
		}
	}
	t.Setenv("PENCODE_CODEX_PATH", "~/.local/bin/codex")
	if got, err := resolveExecutablePath(root, ""); err != nil || got != filepath.Join(home, ".local", "bin", "codex") {
		t.Fatalf("environment path was not expanded: %q, %v", got, err)
	}
	if got, err := resolveExecutablePath(root, absolute); err != nil || got != absolute {
		t.Fatalf("setting did not override environment: %q, %v", got, err)
	}
}

func TestResolveExecutableDirectory(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "Codex CLI")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "codex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(directory, name)
	if err := os.WriteFile(executable, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PENCODE_CODEX_PATH", directory)
	for _, path := range []string{directory, filepath.ToSlash(directory), "./Codex CLI", ""} {
		if got, err := resolveExecutablePath(root, path); err != nil || got != executable {
			t.Fatalf("resolve directory %q = %q, %v; want %q", path, got, err, executable)
		}
	}
	if err := os.Remove(executable); err != nil {
		t.Fatal(err)
	}
	for _, childIsDirectory := range []bool{false, true} {
		if childIsDirectory {
			if err := os.Mkdir(executable, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := resolveExecutablePath(root, directory); err == nil || !strings.Contains(err.Error(), strconv.Quote(directory)) || !strings.Contains(err.Error(), name) {
			t.Fatalf("missing executable diagnostic (directory=%t): %v", childIsDirectory, err)
		}
	}
}

func TestExecutablePathStartsClientAndUpdatesWithoutInterrupting(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	name := "codex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	relative := filepath.Join("tools with spaces", name)
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PENCODE_CODEX_FIXTURE", "1")
	t.Setenv("PENCODE_CODEX_PATH", filepath.Join(root, "missing-codex"))
	a := &agent{ctx: t.Context(), context: peb.ActionContext{ProjectPath: root}}
	update := func(path string) {
		t.Helper()
		value, _ := json.Marshal(path)
		if err := a.handleCommand(command{settings: peb.SettingValues{"executablePath": value}}); err != nil {
			t.Fatal(err)
		}
	}
	update(filepath.ToSlash(relative))
	if err := a.connect(); err != nil {
		t.Fatal(err)
	}
	client := a.client
	defer client.close()
	update(filepath.Join(root, "new-codex"))
	if err := a.connect(); err != nil || a.client != client || client.ctx.Err() != nil {
		t.Fatal("path update interrupted the existing connection", err)
	}
	if _, err := call[threadPage](client, "thread/list", map[string]any{"cwd": root, "sourceKinds": []string{"cli", "vscode", "appServer"}}); err != nil {
		t.Fatal("existing connection stopped responding", err)
	}
	client.close()
	a.client = nil
	update("./" + filepath.ToSlash(filepath.Dir(relative)))
	if err := a.connect(); err != nil {
		t.Fatal("directory setting did not start app-server", err)
	}
	a.client.close()
	a.client = nil
	update(filepath.Join(root, "new-codex"))
	if err := a.connect(); err == nil || !strings.Contains(err.Error(), "new-codex") {
		t.Fatalf("next connection did not use the new path: %v", err)
	}
	update("")
	if err := a.connect(); err == nil || !strings.Contains(err.Error(), "missing-codex") {
		t.Fatalf("clearing the setting did not restore environment fallback: %v", err)
	}
}

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
	fixtureExecutable(t)
	first, second, fresh := t.TempDir(), t.TempDir(), t.TempDir()
	a := &agent{ctx: t.Context()}
	t.Cleanup(a.closeClient)
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
	for _, want := range []*conversation{firstChat, secondChat, firstChat} {
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
