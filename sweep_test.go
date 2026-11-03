package main

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSweepFollowsLatestActivity(t *testing.T) {
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
			t.Fatal("only the latest activity should sweep, without a separate prompt", view.HTML)
		}
		if want {
			ref := latest.groupRef + "/region"
			if latest.groupExpanded {
				ref = "message/" + latest.data.ID
			}
			start := strings.Index(view.HTML, `data-ref="`+ref+`"`)
			sweep := strings.Index(view.HTML, `class="codex-sweep-text"`)
			if start < 0 || sweep < start || strings.Contains(view.HTML[start:sweep], `</section>`) {
				t.Fatal("sweep was applied to an older activity", view.HTML)
			}
		}
	}
	check(true)
	clickActivity(t, a, latest.groupRef)
	check(true)
	clickActivity(t, a, latest.groupRef)
	check(true)
	clickActivity(t, a, latest.groupRef)
	check(true)
	c.requests = []*approval{{id: json.RawMessage(`1`), params: json.RawMessage(`{}`)}}
	check(false)
	c.requests = nil
	check(true)
	for _, status := range []string{"completed", "interrupted", "failed"} {
		c.busy = true
		a.onMessage(envelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"status":"` + status + `"}}`)})
		check(false)
	}
	c.busy = true
	c.items = append(c.items, newChatItem(threadItem{ID: "next-user", Type: "userMessage", Text: "继续"}, true))
	check(false)
}

func TestExpandedSweepMovesWithActivityAndKeepsDetails(t *testing.T) {
	first := newChatItem(threadItem{ID: "first", Type: "reasoning", Text: "检查文件"}, true)
	first.groupExpanded = true
	c := &conversation{thread: thread{ID: "thread"}, busy: true, items: []*chatItem{first}}
	a := &agent{selected: c, conversations: map[string]*conversation{"thread": c}}
	_ = a.composerView()
	latest := newChatItem(threadItem{ID: "latest", Type: "commandExecution", Status: "completed", Command: "cat <file>.go", AggregatedOutput: "<output>"}, true)
	c.items = append(c.items, latest)
	view := a.composerView()
	validateBindings(t, view)
	if strings.Count(view.HTML, `class="codex-sweep-text"`) != 1 || strings.Contains(first.html, `codex-sweep-text`) || !strings.Contains(latest.html, `codex-sweep-text`) {
		t.Fatal("sweep should move to the bottom item even when that item has completed", view.HTML)
	}
	cached := latest.html
	_ = a.composerView()
	if latest.html != cached {
		t.Fatal("unchanged sweeping items should retain their cached HTML")
	}
	clickActivity(t, a, latest.detailRef)
	view = a.composerView()
	validateBindings(t, view)
	if strings.Count(view.HTML, `class="codex-sweep-text"`) != 1 || !strings.Contains(view.HTML, `&lt;output&gt;`) || strings.Contains(view.HTML, `<file>`) || strings.Contains(view.HTML, `&amp;lt;`) {
		t.Fatal("sweep should preserve expanded details and escape labels once", view.HTML)
	}
	latest.replace(threadItem{ID: "latest", Type: "commandExecution", Status: "inProgress", Command: "cat <next>.go", AggregatedOutput: "<next output>"}, false)
	view = a.composerView()
	if strings.Count(view.HTML, `class="codex-sweep-text"`) != 1 || !strings.Contains(view.HTML, `&lt;next output&gt;`) || !latest.expanded || !first.groupExpanded {
		t.Fatal("streaming should preserve the sweep and both expansion levels", view.HTML)
	}
	c.busy = false
	view = a.composerView()
	if strings.Contains(view.HTML, `codex-sweep-text`) || !strings.Contains(view.HTML, `已运行 cat &lt;next&gt;.go`) {
		t.Fatal("ending the turn should restore the plain cached label", view.HTML)
	}
}

func TestSweepEscapesTextAndBoundsAnimationTracks(t *testing.T) {
	var b strings.Builder
	renderSweepText(&b, strings.Repeat("<搜索>", 100))
	if strings.Count(b.String(), `animation-delay:`) > 16 || !utf8.ValidString(b.String()) || strings.Contains(b.String(), "<搜索>") {
		t.Fatal("sweep must stay bounded and escape valid UTF-8", b.String())
	}
}
