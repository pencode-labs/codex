package main

import (
	"os"
	"strings"
	"testing"

	"github.com/pencode-labs/pencode/peb"
)

func TestActivityDetailsSurviveUpdates(t *testing.T) {
	item := threadItem{ID: `cmd"1`, Type: "commandExecution", Status: "inProgress", Command: "echo '<script>'"}
	i := newChatItem(item, false)
	c := &conversation{items: []*chatItem{i}}
	a := &agent{selected: c}
	view := a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, "进行中") || strings.Contains(view.HTML, "<pre>") {
		t.Fatal("running command should show a summary", view.HTML)
	}
	if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "click", Ref: i.groupRef}}}); err != nil {
		t.Fatal(err)
	}
	toggle := func() {
		t.Helper()
		if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "click", Ref: i.detailRef}}}); err != nil {
			t.Fatal(err)
		}
	}
	toggle()
	item.Status, item.AggregatedOutput = "failed", "<script>failed</script>"
	i.replace(item, true)
	view = a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, `aria-expanded="true"`) || !strings.Contains(view.HTML, "&lt;script&gt;failed&lt;/script&gt;") || !strings.Contains(view.HTML, "失败") || strings.Contains(view.HTML, "<script>") {
		t.Fatal("completion lost expanded details or escaping", view.HTML)
	}
	toggle()
	view = a.composerView()
	if strings.Contains(view.HTML, "<pre>") || !strings.Contains(view.HTML, "失败") {
		t.Fatal("failure must remain visible with details collapsed", view.HTML)
	}
}

func TestFileActivityEscapesDiffDetails(t *testing.T) {
	i := newChatItem(threadItem{ID: "file", Type: "fileChange", Status: "completed", Changes: []change{{Path: "<file>.go", Diff: "+<script>"}}}, true)
	c := &conversation{items: []*chatItem{i}}
	a := &agent{selected: c}
	view := a.composerView()
	if strings.Contains(view.HTML, "<pre>") || !strings.Contains(view.HTML, "编辑了文件") {
		t.Fatal(view.HTML)
	}
	i.groupExpanded, i.expanded, i.html = true, true, ""
	view = a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, "&lt;file&gt;.go") || !strings.Contains(view.HTML, "+&lt;script&gt;") {
		t.Fatal("file details were lost or not escaped", view.HTML)
	}
}

func TestHistoryRefreshRetainsPublishedChat(t *testing.T) {
	c := &conversation{thread: thread{ID: "active"}, draft: "draft"}
	a := &agent{selected: c, showHistory: true, history: threadPage{Data: []thread{{ID: "saved", Name: "Saved task"}}}}
	initial := a.composerView()
	prefix := a.composerPrefix
	if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "input", Ref: "prompt", Value: "draft still being typed"}}}); err != nil {
		t.Fatal(err)
	}
	a.history.Data[0].Name = "Updated task"
	refreshed := a.historyView()
	validateBindings(t, refreshed)
	if a.composerPrefix != prefix || !strings.HasPrefix(refreshed.HTML, prefix) || !strings.HasPrefix(initial.HTML, prefix) {
		t.Fatal("refresh regenerated the chat and input")
	}
	if !strings.Contains(refreshed.HTML, "Updated task") || strings.Contains(refreshed.HTML, "Saved task") || c.draft != "draft still being typed" {
		t.Fatal("refresh lost history updates or the current draft")
	}
	if !strings.Contains(a.composerView().HTML, "draft still being typed") {
		t.Fatal("full updates lost the latest draft")
	}
}

func TestHistoryRefreshOnlyDirtiesHistory(t *testing.T) {
	path, _ := os.Executable()
	t.Setenv("PENCODE_CODEX_PATH", path)
	t.Setenv("PENCODE_CODEX_FIXTURE", "1")
	a := &agent{ctx: t.Context(), context: peb.ActionContext{ProjectPath: t.TempDir()}, workspaceOpen: true}
	if err := a.listHistory(""); err != nil {
		t.Fatal(err)
	}
	defer a.client.close()
	if !a.historyDirty || a.workspaceDirty || !a.dirty() {
		t.Fatal("history refresh must schedule only a history update")
	}
}
