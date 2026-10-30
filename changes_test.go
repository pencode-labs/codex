package main

import (
	"encoding/json"
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pencode-labs/pencode/peb"
)

func TestFileChangesAfterTurnCompletion(t *testing.T) {
	for _, status := range []string{"completed", "interrupted", "failed"} {
		t.Run(status, func(t *testing.T) {
			c := &conversation{thread: thread{ID: "thread"}, context: peb.ActionContext{ProjectPath: t.TempDir()}}
			a := &agent{selected: c, workspaceOpen: true, conversations: map[string]*conversation{c.ID: c}}
			notify := func(method string, params any) {
				t.Helper()
				data, err := json.Marshal(params)
				if err != nil {
					t.Fatal(err)
				}
				a.onMessage(envelope{Method: method, Params: data})
			}
			notify("turn/started", map[string]any{"threadId": c.ID, "turn": turn{ID: "one", Status: "inProgress"}})
			edit := threadItem{ID: "edit", Type: "fileChange", Status: "inProgress", Changes: []change{{Path: "src/<file>.go", Diff: "--- a/file.go\n+++ b/file.go\n@@ -1 +1,2 @@\n-old\n+new\n+<script>\n"}}}
			notify("item/started", map[string]any{"threadId": c.ID, "item": edit})
			edit.Status = "completed"
			notify("item/completed", map[string]any{"threadId": c.ID, "item": edit})
			// Completion may be repeated; aggregate each final item only once.
			notify("item/completed", map[string]any{"threadId": c.ID, "item": edit})
			edit.ID = "edit-again"
			edit.Changes[0] = change{Path: filepath.Join(c.context.ProjectPath, "src/<file>.go"), Diff: "+extra\n"}
			notify("item/completed", map[string]any{"threadId": c.ID, "item": edit})
			for _, rejected := range []string{"failed", "declined", "inProgress"} {
				notify("item/completed", map[string]any{"threadId": c.ID, "item": threadItem{ID: rejected, Type: "fileChange", Status: rejected, Changes: []change{{Path: "excluded.go", Diff: "+excluded\n"}}}})
			}
			notify("item/completed", map[string]any{"threadId": c.ID, "item": threadItem{ID: "reply", Type: "agentMessage", Text: "修改完成"}})
			if strings.Contains(a.composerView().HTML, `class="codex-changes"`) {
				t.Fatal("running turn displayed its final change summary")
			}
			notify("turn/completed", map[string]any{"threadId": c.ID, "turn": turn{ID: "one", Status: status}})
			view := a.composerView()
			validateBindings(t, view)
			path := html.EscapeString(filepath.Join("src", "<file>.go"))
			if strings.Count(view.HTML, `class="codex-changes"`) != 1 || !strings.Contains(view.HTML, "已编辑 1 个文件") || !strings.Contains(view.HTML, path) || strings.Contains(view.HTML, "excluded.go") || strings.Contains(view.HTML, "<pre>") {
				t.Fatal("completion did not display only successful edits", view.HTML)
			}
			if strings.Index(view.HTML, "修改完成") > strings.Index(view.HTML, `class="codex-changes"`) {
				t.Fatal("summary must follow the final response")
			}
			first := c.files
			if first.added != 3 || first.removed != 1 || len(first.files[0].diffs) != 2 {
				t.Fatal("same file must merge final edit records", first)
			}
			clickActivity(t, a, "change-file/one/0")
			view = a.composerView()
			validateBindings(t, view)
			if !strings.Contains(view.HTML, "+&lt;script&gt;") || !strings.Contains(view.HTML, "+extra") || strings.Contains(view.HTML, "<script>") {
				t.Fatal("file diff was lost or not escaped", view.HTML)
			}
			clickActivity(t, a, "changes/one")
			view = a.composerView()
			validateBindings(t, view)
			if !strings.Contains(view.HTML, path) || strings.Contains(view.HTML, "<pre>") || !strings.Contains(view.HTML, "展开文件") {
				t.Fatal("file list did not collapse", view.HTML)
			}
			clickActivity(t, a, "changes-diff/one")
			view = a.composerView()
			validateBindings(t, view)
			if !strings.Contains(view.HTML, "收起变更") || !strings.Contains(view.HTML, "<pre>") {
				t.Fatal("view changes must reopen the list and diffs", view.HTML)
			}
			clickActivity(t, a, "changes-diff/one")
			if strings.Contains(a.composerView().HTML, "<pre>") {
				t.Fatal("hide changes did not collapse diffs")
			}
			notify("turn/started", map[string]any{"threadId": c.ID, "turn": turn{ID: "two", Status: "inProgress"}})
			notify("item/completed", map[string]any{"threadId": c.ID, "item": threadItem{ID: "user2", Type: "userMessage", Text: "next task"}})
			notify("turn/completed", map[string]any{"threadId": c.ID, "turn": turn{ID: "two", Status: "completed"}})
			view = a.composerView()
			validateBindings(t, view)
			if strings.Count(view.HTML, `class="codex-changes"`) != 1 || strings.Index(view.HTML, `class="codex-changes"`) > strings.Index(view.HTML, "next task") || len(c.files.files) != 0 {
				t.Fatal("next turn lost or inherited the preceding turn's changes", view.HTML)
			}
			clickActivity(t, a, "changes/one")
			if !first.collapsed {
				t.Fatal("previous turn's summary stopped responding")
			}
		})
	}
}

func TestDiffLineCounts(t *testing.T) {
	for _, tc := range []struct {
		diff           string
		added, removed int
	}{
		{"", 0, 0},
		{"+new\n-old", 1, 1},
		{"--- a/file\n+++ b/file\n@@ -1 +1 @@\n--- content\n+++ content\n context\n", 1, 1},
		{"diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -0,0 +1 @@\n+a\ndiff --git a/b b/b\n--- a/b\n+++ b/b\n@@ -1 +0,0 @@\n-b\n", 1, 1},
	} {
		if added, removed := diffLineCounts(tc.diff); added != tc.added || removed != tc.removed {
			t.Fatalf("counts for %q = +%d -%d, want +%d -%d", tc.diff, added, removed, tc.added, tc.removed)
		}
	}
}

func TestAddedAndDeletedFileCounts(t *testing.T) {
	var item threadItem
	if err := json.Unmarshal([]byte(`{"type":"fileChange","status":"completed","changes":[{"path":"new.go","kind":{"type":"add"},"diff":"package main\n\nfunc main() {}\n"},{"path":"old.go","kind":{"type":"delete"},"diff":"// removed\nlast line"},{"path":"empty.go","kind":{"type":"add"},"diff":""}]}`), &item); err != nil {
		t.Fatal(err)
	}
	f := newTurnFiles("turn", t.TempDir())
	f.add(item)
	if len(f.files) != 3 || f.added != 3 || f.removed != 2 || f.files[0].added != 3 || f.files[1].removed != 2 || f.files[2].added != 0 {
		t.Fatal("added/deleted file contents were counted as unified diffs", f.files)
	}
}

func TestFileChangesCachedWithoutAllocations(t *testing.T) {
	f := newTurnFiles("turn", t.TempDir())
	f.complete = true
	f.add(threadItem{Type: "fileChange", Status: "completed", Changes: []change{{Path: "file.go", Diff: "+new\n-old\n"}}})
	f.prepare()
	if allocations := testing.AllocsPerRun(100, func() {
		f.prepare()
		diffLineCounts("--- a/file\n+++ b/file\n@@ -1 +1 @@\n-old\n+new\n")
	}); allocations != 0 {
		t.Fatalf("cached change summary allocated %g objects", allocations)
	}
}

func TestFileChangesBoundDiffDisplay(t *testing.T) {
	f := newTurnFiles(`turn"`, t.TempDir())
	f.complete = true
	f.add(threadItem{Type: "fileChange", Status: "completed", Changes: []change{{Path: "large.go", Diff: strings.Repeat("+界<&\n", 100000)}}})
	f.collapsed = false
	f.files[0].expanded = true
	f.prepare()
	if !utf8.ValidString(f.html) || len(f.html) > 256<<10 || !strings.Contains(f.html, "部分 diff 已省略") {
		t.Fatal("large diff was unbounded or not valid UTF-8", len(f.html))
	}
}

func TestFileChangesCollapsedPreview(t *testing.T) {
	f := newTurnFiles("turn", t.TempDir())
	f.complete = true
	for _, path := range []string{"one.go", "two.go", "three.go", "four.go", "five.go"} {
		f.add(threadItem{Type: "fileChange", Status: "completed", Changes: []change{{Path: path, Diff: "+new\n"}}})
	}
	reply := newChatItem(threadItem{ID: "reply", Type: "agentMessage", Text: "完成"}, true)
	reply.files = f
	a := &agent{selected: &conversation{files: f, items: []*chatItem{reply}}}
	view := a.composerView()
	validateBindings(t, view)
	if strings.Count(view.HTML, `class="codex-change-file"`) != 3 || strings.Contains(view.HTML, "four.go") || strings.Contains(view.HTML, "five.go") || strings.Contains(view.HTML, "<pre>") || strings.Contains(view.HTML, `data-ref="changes/turn/list" data-pencode-scroll`) || !strings.Contains(view.HTML, "已编辑 5 个文件") {
		t.Fatal("initial preview must show only three files without diffs or a scroll binding", view.HTML)
	}
	clickActivity(t, a, "change-file/turn/0")
	view = a.composerView()
	validateBindings(t, view)
	if strings.Count(view.HTML, `class="codex-change-file"`) != 5 || !strings.Contains(view.HTML, "+new") || f.collapsed {
		t.Fatal("opening a preview file must reveal the full list and its diff", view.HTML)
	}
}

func TestFileChangesMergePathsAndKeepExternalFiles(t *testing.T) {
	root := t.TempDir()
	f := newTurnFiles("turn", root)
	f.add(threadItem{Type: "fileChange", Status: "completed", Changes: []change{
		{Path: "src/../new.go", Diff: "+new\n"},
		{Path: filepath.Join(root, "new.go"), Diff: "+another\n"},
		{Path: "deleted.go", Diff: "-removed\n"},
		{Path: filepath.Join(filepath.Dir(root), "external.go")},
	}})
	if len(f.files) != 3 || f.files[0].path != "new.go" || f.files[0].added != 2 || f.files[1].removed != 1 || !filepath.IsAbs(f.files[2].path) {
		t.Fatal("file paths or counts were not merged correctly", f.files)
	}
}

func TestRunningHistoryCompletesFileChanges(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PENCODE_CODEX_PATH", path)
	t.Setenv("PENCODE_CODEX_FIXTURE", "1")
	t.Setenv("PENCODE_CODEX_TURN_CHANGES", "running")
	a := &agent{ctx: t.Context(), context: peb.ActionContext{ProjectPath: t.TempDir()}, conversations: make(map[string]*conversation), workspaceOpen: true}
	if err := a.resume("saved"); err != nil {
		t.Fatal(err)
	}
	defer a.client.close()
	if !a.selected.busy || strings.Contains(a.composerView().HTML, `class="codex-changes"`) {
		t.Fatal("resuming a running turn displayed final changes")
	}
	a.onMessage(envelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"saved","turn":{"id":"old-turn","status":"completed"}}`)})
	view := a.composerView()
	validateBindings(t, view)
	if strings.Count(view.HTML, `class="codex-changes"`) != 1 || !strings.Contains(view.HTML, "saved.go") || a.selected.files.added != 1 {
		t.Fatal("completing a resumed turn lost prior successful edits", view.HTML)
	}
}

func TestHistoryRestoresFileChanges(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PENCODE_CODEX_PATH", path)
	t.Setenv("PENCODE_CODEX_FIXTURE", "1")
	t.Setenv("PENCODE_CODEX_TURN_CHANGES", "1")
	a := &agent{ctx: t.Context(), context: peb.ActionContext{ProjectPath: t.TempDir()}, conversations: make(map[string]*conversation)}
	if err := a.resume("saved"); err != nil {
		t.Fatal(err)
	}
	defer func() { a.client.close() }()
	for range 2 {
		view := a.composerView()
		validateBindings(t, view)
		if strings.Count(view.HTML, `class="codex-changes"`) != 1 || !strings.Contains(view.HTML, "saved.go") || strings.Contains(view.HTML, "已处理") {
			t.Fatal("history lost file changes or estimated an absent duration", view.HTML)
		}
		clickActivity(t, a, "change-file/old-turn/0")
		if !strings.Contains(a.composerView().HTML, "+stored") {
			t.Fatal("restored diff did not open")
		}
		a.selectConversation(nil)
		if a.conversations["saved"].files != nil || a.conversations["saved"].items != nil {
			t.Fatal("leaving an idle conversation retained its diffs")
		}
		if err := a.resume("saved"); err != nil {
			t.Fatal(err)
		}
	}
}
