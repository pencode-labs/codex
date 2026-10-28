package main

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSweepFollowsLatestCollapsedActivity(t *testing.T) {
	first := newChatItem(threadItem{ID: "first", Type: "reasoning"}, true)
	latest := newChatItem(threadItem{ID: "latest", Type: "fileChange"}, true)
	c := &conversation{thread: thread{ID: "thread"}, busy: true, items: []*chatItem{
		first,
		newChatItem(threadItem{ID: "reply", Type: "agentMessage", Text: "检查文件"}, true),
		latest,
		newChatItem(threadItem{ID: "reply2", Type: "agentMessage", Text: "已修改文件"}, true),
	}}
	a := &agent{selected: c, conversations: map[string]*conversation{"thread": c}}
	check := func(want bool) {
		t.Helper()
		view := a.composerView()
		validateBindings(t, view)
		count := strings.Count(view.HTML, `class="codex-sweep-text"`)
		if count > 1 || (count == 1) != want || strings.Contains(view.HTML, `data-ref="progress"`) || strings.Contains(view.HTML, "正在处理") {
			t.Fatal("only the latest collapsed activity should sweep, without a separate prompt", view.HTML)
		}
		if want {
			start := strings.Index(view.HTML, `data-ref="`+latest.groupRef+`/region"`)
			sweep := strings.Index(view.HTML, `class="codex-sweep-text"`)
			if start < 0 || sweep < start {
				t.Fatal("sweep was applied to an older activity", view.HTML)
			}
		}
	}
	check(true)
	clickActivity(t, a, latest.groupRef)
	check(false)
	clickActivity(t, a, latest.groupRef)
	check(true)
	c.requests = []*approval{{id: json.RawMessage(`1`), params: json.RawMessage(`{}`)}}
	check(false)
	c.requests = nil
	for _, status := range []string{"completed", "interrupted", "failed"} {
		c.busy = true
		a.onMessage(envelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"status":"` + status + `"}}`)})
		check(false)
	}
	c.busy = true
	c.items = append(c.items, newChatItem(threadItem{ID: "next-user", Type: "userMessage", Text: "继续"}, true))
	check(false)
}

func TestSweepEscapesTextAndBoundsAnimationTracks(t *testing.T) {
	var b strings.Builder
	renderSweepText(&b, strings.Repeat("<搜索>", 100))
	if strings.Count(b.String(), `animation-delay:`) > 16 || !utf8.ValidString(b.String()) || strings.Contains(b.String(), "<搜索>") {
		t.Fatal("sweep must stay bounded and escape valid UTF-8", b.String())
	}
}
