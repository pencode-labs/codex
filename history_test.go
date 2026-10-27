package main

import (
	"os"
	"slices"
	"testing"

	"github.com/pencode-labs/pencode/peb"
)

func TestHistoryCompactionPreservesOrderWithoutAllocations(t *testing.T) {
	data := []thread{{ID: "first"}, {ID: "second"}, {ID: "first"}, {}, {ID: "third"}, {ID: "second"}}
	page := threadPage{Data: make([]thread, len(data)), NextCursor: "next"}
	if allocations := testing.AllocsPerRun(100, func() {
		page.Data = page.Data[:len(data)]
		copy(page.Data, data)
		page.compact()
	}); allocations != 0 {
		t.Fatalf("history compaction allocates: %g", allocations)
	}
	if !slices.Equal(page.Data, []thread{{ID: "first"}, {ID: "second"}, {ID: "third"}}) || page.NextCursor != "next" {
		t.Fatal("compaction lost order or cursor", page)
	}
	for _, entry := range page.Data[len(page.Data):cap(page.Data)] {
		if entry != (thread{}) {
			t.Fatal("compaction retained removed entries", entry)
		}
	}
}

func TestHistoryFollowsProjectChanges(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PENCODE_CODEX_PATH", path)
	t.Setenv("PENCODE_CODEX_FIXTURE", "1")
	t.Setenv("PENCODE_CODEX_HISTORY_PROJECTS", "1")
	first, second := t.TempDir(), t.TempDir()
	a := &agent{ctx: t.Context(), conversations: make(map[string]*conversation), drafts: map[string]string{first: "first draft", second: "second draft"}}
	if err := a.setContext(peb.ActionContext{ID: "chat", ProjectPath: first}); err != nil {
		t.Fatal(err)
	}
	if err := a.listHistory(""); err != nil {
		t.Fatal(err)
	}
	defer a.client.close()
	if err := a.resume(first + "/saved"); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{second, first, second, "", first} {
		err := a.handleCommand(command{event: peb.UIEvent{
			ID: "composer", Context: peb.ActionContext{ID: "chat", ProjectPath: root},
			Event: peb.EditorEvent{Kind: "projectchange"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if a.context.ProjectPath != root || a.selected != nil {
			t.Fatal("project change retained the previous conversation", a.context, a.selected)
		}
		if root == "" {
			if len(a.history.Data) != 0 || a.showHistory {
				t.Fatal("closing the last project retained history", a.history)
			}
			continue
		}
		if len(a.history.Data) != 1 || a.history.Data[0].ID != root+"/saved" || !a.showHistory {
			t.Fatal("project change loaded another project's history", a.history)
		}
		view := a.composerView()
		validateBindings(t, view)
		if a.drafts[root] == "" {
			t.Fatal("project change lost the draft")
		}
	}
}
