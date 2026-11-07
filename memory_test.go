package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pencode-labs/pencode/peb"
)

func TestColdConversationBudgetPreservesLiveState(t *testing.T) {
	a := &agent{workspaceOpen: true, conversations: make(map[string]*conversation)}
	for index := range 9 {
		c := &conversation{thread: thread{ID: fmt.Sprint(index)}, loaded: true, viewed: uint64(index), draft: "keep draft"}
		c.items = []*chatItem{newChatItem(threadItem{Type: "agentMessage", Text: strings.Repeat("x", 128<<10)}, true)}
		c.items[0].html = strings.Repeat("cached", 10000)
		a.conversations[c.ID] = c
	}
	live := a.conversations["0"]
	live.busy = true
	pending := a.conversations["1"]
	pending.requests = []*approval{{method: "pending"}}
	editing := a.conversations["2"]
	editing.editing = editing.items[0]
	a.selected = a.conversations["7"]
	a.trimConversations()
	bytes, count := 0, 0
	for _, c := range a.conversations {
		if c == live || c == pending || c == editing || c == a.selected {
			if !c.loaded || len(c.items) != 1 {
				t.Fatal("budget discarded live, approval, editing or visible state")
			}
			continue
		}
		if len(c.items) != 0 {
			bytes += c.transcriptBytes()
			count++
			if c.items[0].html != "" {
				t.Fatal("inactive conversation retained paint")
			}
		}
		if c.draft != "keep draft" {
			t.Fatal("budget discarded a draft")
		}
	}
	if count > 4 || bytes > 1<<20 || a.conversations["3"].loaded {
		t.Fatal("cold cache exceeded budget or evicted a newer conversation")
	}
	// Switching drops the cached full view immediately, including when hidden.
	a.composerPrefix = strings.Repeat("old transcript", 1000)
	a.selectConversation(live)
	if a.composerPrefix != "" {
		t.Fatal("switch retained the previous full view")
	}
}

func TestLargeClosedConversationReloadsHistory(t *testing.T) {
	fixtureExecutable(t)
	a := &agent{publisher: &capturedUI{}, ctx: t.Context(), context: peb.ActionContext{ProjectPath: t.TempDir()}, conversations: make(map[string]*conversation), workspaceOpen: true}
	t.Cleanup(a.closeClient)
	if err := a.resume("saved"); err != nil {
		t.Fatal(err)
	}
	c := a.selected
	c.draft = "keep draft"
	for range 12 {
		c.items = append(c.items, newChatItem(threadItem{Type: "agentMessage", Text: strings.Repeat("x", 128<<10)}, true))
	}
	if err := a.closeWorkspace(); err != nil {
		t.Fatal(err)
	}
	if c.loaded || len(c.items) != 0 || c.draft != "keep draft" {
		t.Fatal("closed transcript was not unloaded independently of its draft")
	}
	a.workspaceOpen = true
	if err := a.resume(c.ID); err != nil || a.selected != c || !c.loaded || len(c.items) == 0 || c.draft != "keep draft" {
		t.Fatal("unloaded history did not restore", err)
	}
}

func BenchmarkComposerStreamingMemory(b *testing.B) {
	c := &conversation{thread: thread{ID: "thread"}}
	for index := range 100 {
		c.items = append(c.items, newChatItem(threadItem{ID: fmt.Sprint(index), Type: "agentMessage", Text: strings.Repeat("text ", 100)}, true))
	}
	a := &agent{selected: c, workspaceOpen: true}
	a.composerView()
	b.ReportAllocs()
	for b.Loop() {
		a.composerView()
	}
}
