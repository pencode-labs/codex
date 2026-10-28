package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/pencode-labs/pencode/peb"
)

func TestTitleGenerationUsesIsolatedStructuredTurn(t *testing.T) {
	path, _ := os.Executable()
	t.Setenv("PENCODE_CODEX_FIXTURE", "1")
	for _, prompt := range []string{"实现一个功能", "invalid title", "empty title", "title failure"} {
		t.Run(prompt, func(t *testing.T) {
			title, err := requestTitle(t.Context(), t.TempDir(), path, prompt)
			if prompt == "实现一个功能" {
				if err != nil || title != "实现功能" {
					t.Fatalf("title = %q, err = %v", title, err)
				}
			} else if err == nil || title != "" {
				t.Fatalf("invalid generation was accepted: %q, %v", title, err)
			}
		})
	}
}

func TestGeneratedTitlePersistsForInactiveConversation(t *testing.T) {
	path, _ := os.Executable()
	t.Setenv("PENCODE_CODEX_PATH", path)
	t.Setenv("PENCODE_CODEX_FIXTURE", "1")
	job := &titleJob{id: "saved", title: "自动标题"}
	c := &conversation{thread: thread{ID: "saved"}, titleJob: job}
	a := &agent{ctx: t.Context(), context: peb.ActionContext{ProjectPath: t.TempDir()}, conversations: map[string]*conversation{c.ID: c}, history: threadPage{Data: []thread{c.thread}}}
	if err := a.finishTitle(job); err != nil || c.Name != job.title || a.history.Data[0].Name != job.title || !a.historyDirty || a.selected != nil {
		t.Fatal("inactive title was not saved", err, c.Name, a.history)
	}
	defer a.client.close()
	if err := a.resume(c.ID); err != nil || a.selected.Name != job.title {
		t.Fatal("generated title did not survive resume", err, c.Name)
	}
}

func TestManualTitleCancelsGenerationAndRejectsLateResult(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	job := &titleJob{id: "saved", title: "自动标题", cancel: cancel}
	c := &conversation{thread: thread{ID: "saved"}, titleJob: job, draft: "draft", busy: true}
	a := &agent{selected: c, conversations: map[string]*conversation{c.ID: c}, history: threadPage{Data: []thread{c.thread, {ID: "unloaded"}}}}
	params, _ := json.Marshal(map[string]string{"threadId": c.ID, "threadName": "用户标题"})
	a.onMessage(envelope{Method: "thread/name/updated", Params: params})
	if ctx.Err() == nil || c.titleJob != nil {
		t.Fatal("manual title did not cancel generation")
	}
	if err := a.finishTitle(job); err != nil || c.Name != "用户标题" || a.history.Data[0].Name != "用户标题" || c.draft != "draft" || !c.busy {
		t.Fatal("late generation changed the manual title or conversation", err)
	}
	params, _ = json.Marshal(map[string]string{"threadId": "unloaded", "threadName": "历史标题"})
	a.onMessage(envelope{Method: "thread/name/updated", Params: params})
	if a.history.Data[1].Name != "历史标题" || a.selected != c {
		t.Fatal("notification for unloaded history was lost")
	}
	if n := testing.AllocsPerRun(100, func() { a.updateThreadName(c.ID, "用户标题") }); n != 0 {
		t.Fatalf("updating a title allocates: %g", n)
	}
}

func TestTitleGenerationCanBeCancelledWithoutBlockingActor(t *testing.T) {
	path, _ := os.Executable()
	t.Setenv("PENCODE_CODEX_FIXTURE", "1")
	gate := t.TempDir() + "/gate"
	t.Setenv("PENCODE_CODEX_TITLE_GATE", gate)
	c := &conversation{thread: thread{ID: "saved"}, context: peb.ActionContext{ProjectPath: t.TempDir()}}
	a := &agent{ctx: t.Context(), executablePath: path, commands: make(chan command, 1), conversations: map[string]*conversation{c.ID: c}}
	a.generateTitle(c, "任务")
	if c.title() != "正在生成标题…" {
		t.Fatal("missing pending title")
	}
	job := c.titleJob
	t.Cleanup(job.cancel)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(gate + ".waiting"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("title worker did not reach the pending inference request")
		}
		time.Sleep(time.Millisecond)
	}
	a.selected = c
	if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "input", Ref: "prompt", Value: "继续编辑"}}}); err != nil || c.draft != "继续编辑" {
		t.Fatal("pending generation blocked the composer", err)
	}
	a.updateThreadName(c.ID, "手动标题")
	select {
	case cmd := <-a.commands:
		if cmd.title != job || !errors.Is(job.err, context.Canceled) {
			t.Fatal("cancelled worker did not finish", job.err)
		}
		if err := a.handleCommand(cmd); err != nil || c.Name != "手动标题" {
			t.Fatal("cancelled result changed the title", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("title generation blocked cancellation")
	}
}
