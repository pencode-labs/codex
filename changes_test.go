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
			if strings.Contains(a.composerView().HTML, `class="codex-live-changes"`) {
				t.Fatal("empty turn displayed a change pill")
			}
			edit := threadItem{ID: "edit", Type: "fileChange", Status: "inProgress", Changes: []change{{Path: "src/<file>.go", Diff: "--- a/file.go\n+++ b/file.go\n@@ -1 +1,2 @@\n-old\n+new\n+<script>\n"}}}
			notify("item/started", map[string]any{"threadId": c.ID, "item": edit})
			if strings.Contains(a.composerView().HTML, `class="codex-live-changes"`) {
				t.Fatal("pending edit was counted before success")
			}
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
			running := a.composerView()
			validateBindings(t, running)
			name := html.EscapeString("<file>.go")
			path := html.EscapeString(filepath.Join("src", "<file>.go"))
			if !strings.Contains(running.HTML, `class="codex-change-path" title="`+path+`">`+name+`</span>`) {
				t.Fatal("live changes must show only the complete filename", running.HTML)
			}
			if !strings.Contains(running.HTML, `class="codex-live-changes"`) || !strings.Contains(running.HTML, "1 个文件已更改") || !strings.Contains(running.HTML, `title="`+html.EscapeString(filepath.Join("src", "<file>.go"))+`"`) || strings.Contains(running.HTML, "excluded.go") || c.files.added != 3 || c.files.removed != 1 {
				t.Fatal("running summary lost, duplicated, or failed to escape successful edits", running.HTML)
			}
			notify("turn/completed", map[string]any{"threadId": c.ID, "turn": turn{ID: "one", Status: status}})
			view := a.composerView()
			if strings.Contains(view.HTML, `class="codex-live-changes"`) {
				t.Fatal("completed turn retained its live pill")
			}
			validateBindings(t, view)
			if !strings.Contains(view.HTML, `class="codex-change-path">`+name+`</span>`) {
				t.Fatal("completed changes must show only the complete filename", view.HTML)
			}
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
			view = changeViewForClick(t, a, "change-file/one/0")
			validateBindings(t, view)
			if !strings.Contains(view.HTML, "+&lt;script&gt;") || !strings.Contains(view.HTML, "+extra") || strings.Contains(view.HTML, "<script>") {
				t.Fatal("file diff was lost or not escaped", view.HTML)
			}
			clickActivity(t, a, "changes/one")
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

func TestDiffHighlighting(t *testing.T) {
	for _, tc := range []struct {
		name, text, kind string
		want             []string
	}{
		{"unified", "--- a/file\n+++ b/file\n@@ -1,2 +1,2 @@\n--- old\n+++ <new>\n context\n", "update", []string{
			`<span class="codex-diff-header">--- a/file` + "\n+++ b/file\n</span>",
			`<span class="codex-diff-hunk">@@ -1,2 +1,2 @@` + "\n</span>",
			`<span class="codex-diff-removed">--- old` + "\n</span>",
			`<span class="codex-diff-added">+++ &lt;new&gt;` + "\n</span> context\n",
		}},
		{"headerless", "+<script>\n-old", "", []string{
			`<span class="codex-diff-added">+&lt;script&gt;` + "\n</span>",
			`<span class="codex-diff-removed">-old</span>`,
		}},
		{"added file", "plain\n-minus\n@@ content\n", "add", []string{
			`<span class="codex-diff-added">plain` + "\n-minus\n@@ content\n</span>",
		}},
		{"deleted file", "plain\n+plus\n", "delete", []string{
			`<span class="codex-diff-removed">plain` + "\n+plus\n</span>",
		}},
		{"multiple files", "@@ -1 +1 @@\n-old\n+new\ndiff --git a/b b/b\n--- a/b\n+++ b/b\n", "update", []string{
			`<span class="codex-diff-header">diff --git a/b b/b` + "\n--- a/b\n+++ b/b\n</span>",
		}},
		{"context", " context\n\n last", "", nil},
		{"empty", "", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			renderDiff(&b, tc.text, tc.kind)
			for _, want := range tc.want {
				if !strings.Contains(b.String(), want) {
					t.Fatalf("missing highlight %q in %s", want, b.String())
				}
			}
			plain := strings.NewReplacer(`<pre><code class="codex-diff">`, "", `</code></pre>`, "", `</span>`, "",
				`<span class="codex-diff-added">`, "", `<span class="codex-diff-removed">`, "",
				`<span class="codex-diff-header">`, "", `<span class="codex-diff-hunk">`, "").Replace(b.String())
			if html.UnescapeString(plain) != tc.text {
				t.Fatal("highlighting changed the diff text", b.String())
			}
		})
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
	live := newTurnFiles("running", t.TempDir())
	live.add(threadItem{Type: "fileChange", Status: "completed", Changes: []change{{Path: "file.go", Diff: "+new\n"}}})
	live.prepareLive()
	if allocations := testing.AllocsPerRun(100, func() {
		f.prepare()
		live.prepareLive()
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
	f.files[0].diffs[0].text = strings.Repeat("+\n-\n", 100000)
	f.html = ""
	f.prepare()
	if len(f.html) > 256<<10 || !strings.Contains(f.html, "部分 diff 已省略") {
		t.Fatal("highlight markup exceeded the display budget", len(f.html))
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
	view = changeViewForClick(t, a, "change-file/turn/0")
	validateBindings(t, view)
	if strings.Count(view.HTML, `class="codex-change-file"`) != 3 || !strings.Contains(view.HTML, "<pre>") || !f.collapsed || !strings.Contains(view.HTML, "+new") {
		t.Fatal("expanding one diff must preserve the three-file preview", view.HTML)
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
	fixtureExecutable(t)
	t.Setenv("PENCODE_CODEX_TURN_CHANGES", "running")
	a := &agent{ctx: t.Context(), context: peb.ActionContext{ProjectPath: t.TempDir()}, conversations: make(map[string]*conversation), workspaceOpen: true}
	if err := a.resume("saved"); err != nil {
		t.Fatal(err)
	}
	defer a.client.close()
	if !a.selected.busy || strings.Contains(a.composerView().HTML, `class="codex-changes"`) {
		t.Fatal("resuming a running turn displayed final changes")
	}
	if !strings.Contains(a.composerView().HTML, "1 个文件已更改") || a.selected.files.added != 1 {
		t.Fatal("resuming a running turn lost its live changes")
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
	fixtureExecutable(t)
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
		view = changeViewForClick(t, a, "change-file/old-turn/0")
		if !strings.Contains(view.HTML, "+stored") {
			t.Fatal("restored diff did not open")
		}
		clickActivity(t, a, "change-file/old-turn/0")
		a.selectConversation(nil)
		if len(a.conversations["saved"].items) == 0 {
			t.Fatal("leaving a conversation lost its cached diffs")
		}
		if err := a.resume("saved"); err != nil {
			t.Fatal(err)
		}
	}
}

func changeViewForClick(t *testing.T, a *agent, ref string) *peb.EditorView {
	t.Helper()
	updates := make(chan peb.UI, 2)
	previous := a.publisher
	a.publisher = syncPublisher{updates: updates}
	defer func() { a.publisher = previous }()
	clickActivity(t, a, ref)
	if len(updates) != 0 {
		t.Fatal("file click must expand inline without publishing another surface")
	}
	if !a.selected.dirty {
		t.Fatal("file click did not invalidate the conversation")
	}
	view := a.composerView()
	validateBindings(t, view)
	return view
}

func TestLiveFileChangesExpandInline(t *testing.T) {
	root := t.TempDir()
	f := newTurnFiles(`turn/"one`, root)
	edit := threadItem{Type: "fileChange", Status: "completed", Changes: []change{
		{Path: "src/<file>.go", Diff: "-old\n+<new>\n"},
		{Path: "test/<file>.go", Diff: "deleted\n"},
	}}
	edit.Changes[1].Kind.Type = "delete"
	f.add(edit)
	c := &conversation{thread: thread{ID: "thread/one"}, context: peb.ActionContext{ProjectPath: root}, files: f, busy: true}
	a := &agent{selected: c}
	view := a.composerView()
	validateBindings(t, view)
	if len(f.liveBindings) != 2 || !strings.Contains(view.HTML, `<button type="button" class="codex-live-change" data-ref="change-file/turn/&#34;one/0"`) {
		t.Fatal("live file rows must have click bindings", view.HTML, f.liveBindings)
	}
	view = changeViewForClick(t, a, `change-file/turn/"one/0`)
	if !strings.Contains(view.HTML, "+&lt;new&gt;") || strings.Contains(view.HTML, "<new>") || !f.files[0].expanded || f.files[1].expanded {
		t.Fatal("only the clicked file must expand with an escaped diff", view.HTML)
	}
	view = changeViewForClick(t, a, `change-file/turn/"one/1`)
	if !strings.Contains(view.HTML, `class="codex-diff-removed">deleted`) || !f.files[0].expanded || !f.files[1].expanded {
		t.Fatal("same-name files must expand independently", view.HTML)
	}
	f.add(threadItem{Type: "fileChange", Status: "completed", Changes: []change{{Path: "src/<file>.go", Diff: "+next\n"}}})
	view = a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, "+next") || f.complete || !f.collapsed || !c.busy || len(f.liveBindings) != 2 {
		t.Fatal("live updates must preserve expanded diffs and running state", view.HTML)
	}
	view = changeViewForClick(t, a, `change-file/turn/"one/0`)
	if f.files[0].expanded || !f.files[1].expanded || strings.Contains(view.HTML, "+next") {
		t.Fatal("clicking again must collapse only that file", view.HTML)
	}
	view = changeViewForClick(t, a, `change-file/turn/"one/1`)
	if strings.Contains(view.HTML, "<pre>") {
		t.Fatal("all diffs should now be collapsed", view.HTML)
	}
	if allocations := testing.AllocsPerRun(100, func() { c.handleChangesClick(`change-file/turn/"one/0`) }); allocations != 0 {
		t.Fatalf("file toggle allocated %g objects", allocations)
	}
	updates := make(chan peb.UI, 2)
	a.publisher = syncPublisher{updates: updates}
	for _, ref := range []string{"change-file/missing/0", `change-file/turn/"one/-1`, `change-file/turn/"one/2`, `change-file/turn/"one/invalid`} {
		clickActivity(t, a, ref)
	}
	if len(updates) != 0 {
		t.Fatal("invalid file clicks opened a preview")
	}
}

func TestInlineFileChangeBounds(t *testing.T) {
	for _, diff := range []string{strings.Repeat("+界<&\n", 100000), strings.Repeat("+\n-\n", 100000)} {
		f := newTurnFiles("turn", t.TempDir())
		f.add(threadItem{Type: "fileChange", Status: "completed", Changes: []change{{Path: "large.go", Diff: diff}}})
		a := &agent{selected: &conversation{files: f}}
		a.selected.busy = true
		view := changeViewForClick(t, a, "change-file/turn/0")
		if !utf8.ValidString(view.HTML) || len(view.HTML) > 256<<10 || !strings.Contains(view.HTML, "部分 diff 已省略") {
			t.Fatal("large preview must remain valid UTF-8 and explain truncation")
		}
		if _, err := peb.EncodeUI(nil, peb.UI{ID: "composer", Title: "Codex", Kind: peb.EditorTab, View: view}); err != nil {
			t.Fatal("preview exceeded the extension transport budget", err)
		}
	}
}
