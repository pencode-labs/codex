package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/pencode-labs/pencode/peb"
)

func clickActivity(t *testing.T, a *agent, ref string) {
	t.Helper()
	if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "click", Ref: ref}}}); err != nil {
		t.Fatal(err)
	}
}

func TestActivityGroupThreeLevelsAndStreaming(t *testing.T) {
	thinking := newChatItem(threadItem{ID: "thinking", Type: "reasoning", Summary: []string{"Check the build"}}, true)
	cmd := newChatItem(threadItem{ID: "cmd", Type: "commandExecution", Command: "go test ./...", Status: "inProgress"}, false)
	c := &conversation{thread: thread{ID: "thread"}, items: []*chatItem{thinking, cmd}}
	a := &agent{selected: c, conversations: map[string]*conversation{"thread": c}}
	view := a.composerView()
	validateBindings(t, view)
	if strings.Count(view.HTML, `class="codex-activity-summary"`) != 1 || strings.Contains(view.HTML, "go test") || !strings.Contains(view.HTML, "进行中") {
		t.Fatal("group should start collapsed with running state", view.HTML)
	}
	region := `data-ref="` + thinking.groupRef + `/region" data-pencode-region`
	if !strings.Contains(view.HTML, region) {
		t.Fatal("activity updates must be isolated from the surrounding transcript", view.HTML)
	}
	clickActivity(t, a, thinking.groupRef)
	view = a.composerView()
	validateBindings(t, view)
	if strings.Count(view.HTML, `class="codex-activity-summary"`) != 3 || !strings.Contains(view.HTML, "已运行 go test ./...") || strings.Contains(view.HTML, "<pre>") {
		t.Fatal("group should reveal collapsed item summaries", view.HTML)
	}
	if !strings.Contains(view.HTML, region) {
		t.Fatal("expanding a group changed its region identity", view.HTML)
	}
	clickActivity(t, a, cmd.detailRef)
	a.onMessage(envelope{Method: "item/commandExecution/outputDelta", Params: json.RawMessage(`{"threadId":"thread","itemId":"cmd","delta":"<test output>"}`)})
	view = a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, "&lt;test output&gt;") || !cmd.expanded || !thinking.groupExpanded {
		t.Fatal("streaming should retain both expansion levels and escape output", view.HTML)
	}
	c.items = append(c.items, newChatItem(threadItem{ID: "next", Type: "commandExecution", Command: "go vet ./...", Status: "inProgress"}, false))
	view = a.composerView()
	if strings.Count(view.HTML, `class="codex-activity-group"`) != 1 || !strings.Contains(view.HTML, "已运行 go vet ./...") {
		t.Fatal("new activity should join the expanded group", view.HTML)
	}
	clickActivity(t, a, thinking.groupRef)
	cmd.replace(threadItem{ID: "cmd", Type: "commandExecution", Command: "go test ./...", Status: "failed", AggregatedOutput: "<failed>"}, true)
	view = a.composerView()
	validateBindings(t, view)
	if strings.Contains(view.HTML, "<pre>") || strings.Contains(view.HTML, `data-ref="item/cmd"`) || !strings.Contains(view.HTML, "失败") {
		t.Fatal("collapsed group should hide details and their bindings but retain failure", view.HTML)
	}
	clickActivity(t, a, thinking.groupRef)
	view = a.composerView()
	if !strings.Contains(view.HTML, "&lt;failed&gt;") {
		t.Fatal("collapsing the group should preserve the child expansion", view.HTML)
	}
}

func TestActivityGroupsRespectMessagesAndKeepApprovalsVisible(t *testing.T) {
	c := &conversation{items: []*chatItem{
		newChatItem(threadItem{ID: "a", Type: "commandExecution", Command: "pwd"}, true),
		newChatItem(threadItem{ID: "reply", Type: "agentMessage", Text: "Checking files"}, true),
		newChatItem(threadItem{ID: "b", Type: "reasoning", Text: "Thinking"}, true),
		newChatItem(threadItem{ID: "user", Type: "userMessage", Text: "Continue"}, true),
		newChatItem(threadItem{ID: "c", Type: "webSearch", Query: "docs"}, true),
	}, requests: []*approval{{id: json.RawMessage(`1`), method: "item/commandExecution/requestApproval", params: json.RawMessage(`{}`)}}}
	a := &agent{selected: c}
	view := a.composerView()
	validateBindings(t, view)
	if strings.Count(view.HTML, `class="codex-activity-group"`) != 3 || !strings.Contains(view.HTML, "Checking files") || !strings.Contains(view.HTML, "Continue") || !strings.Contains(view.HTML, `data-ref="accept/1"`) {
		t.Fatal("messages should separate groups and approvals must remain visible", view.HTML)
	}
}

func TestReasoningGroupPreviewsFirstSummaryLine(t *testing.T) {
	i := newChatItem(threadItem{ID: "thinking", Type: "reasoning", Summary: []string{"\n**检查 <入口>**\r\n后续分析", "另一条摘要"}}, true)
	a := &agent{selected: &conversation{items: []*chatItem{i}}}
	for _, expanded := range []bool{false, true} {
		i.groupExpanded = expanded
		view := a.composerView()
		validateBindings(t, view)
		want := 1
		if expanded {
			want = 3 // Group, item label and accessible item name.
		}
		if strings.Count(view.HTML, "检查 &lt;入口&gt;") != want || strings.Contains(view.HTML, "思考摘要") || strings.Contains(view.HTML, "后续分析") || strings.Contains(view.HTML, "另一条摘要") {
			t.Fatal("reasoning titles should preview only the escaped first line", view.HTML)
		}
	}
	clickActivity(t, a, i.detailRef)
	view := a.composerView()
	if !strings.Contains(view.HTML, "后续分析") || !strings.Contains(view.HTML, "另一条摘要") {
		t.Fatal("expanding reasoning should retain its complete summary", view.HTML)
	}
}

func TestReasoningGroupTitleUpdatesWithStream(t *testing.T) {
	i := newChatItem(threadItem{ID: "thinking", Type: "reasoning"}, false)
	c := &conversation{thread: thread{ID: "thread"}, items: []*chatItem{i}}
	a := &agent{selected: c, conversations: map[string]*conversation{"thread": c}}
	_ = a.composerView()
	for _, delta := range []string{"检查入口", "\n后续分析"} {
		a.onMessage(envelope{Method: "item/reasoning/summaryTextDelta", Params: json.RawMessage(`{"threadId":"thread","itemId":"thinking","delta":` + strconv.Quote(delta) + `}`)})
		view := a.composerView()
		if !strings.Contains(view.HTML, ">检查入口</span>") || strings.Contains(view.HTML, "后续分析") || strings.Contains(view.HTML, "思考摘要") {
			t.Fatal("streamed reasoning should update the collapsed group title", view.HTML)
		}
	}
}

func TestActivityCommandActionsAndFileSummary(t *testing.T) {
	var item threadItem
	if err := json.Unmarshal([]byte(`{"id":"actions","type":"commandExecution","command":"cat <file>.go && ls && rg '<query>' src && go test ./...","status":"completed","commandActions":[{"type":"read","name":"<file>.go","path":"src/<file>.go","command":"cat <file>.go"},{"type":"listFiles","path":"src","command":"ls src"},{"type":"search","path":"src","query":"<query>","command":"rg '<query>' src"},{"type":"unknown","command":"go test ./..."}]}`), &item); err != nil {
		t.Fatal(err)
	}
	cmd := newChatItem(item, true)
	file := newChatItem(threadItem{ID: "edit", Type: "fileChange", Status: "completed", Changes: []change{{Path: "src/<file>.go", Diff: "--- a/file.go\n+++ b/file.go\n@@ -1,2 +1,3 @@\n-old\n+new\n+extra\n context\n"}}}, true)
	a := &agent{selected: &conversation{items: []*chatItem{cmd, file}}}
	view := a.composerView()
	if !strings.Contains(view.HTML, "编辑了文件、读取了文件、列出了文件、进行了搜索、运行了命令") || strings.Contains(view.HTML, "思考摘要") {
		t.Fatal("group should summarize each action kind once", view.HTML)
	}
	clickActivity(t, a, cmd.groupRef)
	view = a.composerView()
	validateBindings(t, view)
	for _, want := range []string{"已读取 &lt;file&gt;.go", "已列出 src 中的文件", "已在 src 中搜索“&lt;query&gt;”", "已运行 go test ./...", "已编辑 &lt;file&gt;.go +2 -1", "book-open.svg", "pencil.svg"} {
		if !strings.Contains(view.HTML, want) {
			t.Fatalf("missing %q in %s", want, view.HTML)
		}
	}
	if strings.Contains(view.HTML, "<pre>") || strings.Contains(view.HTML, "<file>") || strings.Contains(view.HTML, "<query>") {
		t.Fatal("summaries must escape text and keep details collapsed", view.HTML)
	}
	clickActivity(t, a, file.detailRef)
	view = a.composerView()
	if !strings.Contains(view.HTML, "src/&lt;file&gt;.go") || !strings.Contains(view.HTML, "@@ -1,2 +1,3 @@") {
		t.Fatal("third level should show the full path and diff", view.HTML)
	}
}
