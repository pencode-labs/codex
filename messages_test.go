package main

import (
	"bytes"
	"encoding/json"
	"html"
	"strings"
	"testing"
	"time"

	"github.com/pencode-labs/pencode/peb"
)

func messageEditFixture(t *testing.T, responses ...envelope) (*agent, *conversation, *bytes.Buffer) {
	t.Helper()
	prior := newChatItem(threadItem{ID: "prior", Type: "userMessage", Text: "prior prompt"}, true)
	prior.files = newTurnFiles("prior-turn", "")
	target := newChatItem(threadItem{ID: `target"`, Type: "userMessage", Text: "original\n**任务**"}, true)
	target.files = newTurnFiles("edit-turn", "")
	target.sentAt = time.Date(2026, 11, 4, 10, 6, 0, 0, time.Local)
	reply := newChatItem(threadItem{ID: "reply", Type: "agentMessage", Text: "old reply"}, true)
	reply.files = target.files
	later := newChatItem(threadItem{ID: "later", Type: "userMessage", Text: "later prompt"}, true)
	later.files = newTurnFiles("later-turn", "")
	c := &conversation{thread: thread{ID: "thread", Name: "task"}, loaded: true, draft: "keep composer draft", items: []*chatItem{prior, target, reply, later}, cursor: "old-cursor"}
	var output bytes.Buffer
	client := &rpcClient{ctx: t.Context(), writer: json.NewEncoder(&output), messages: make(chan envelope, len(responses))}
	for _, response := range responses {
		client.messages <- response
	}
	a := &agent{selected: c, client: client, conversations: map[string]*conversation{c.ID: c}}
	client.handle = a.onMessage
	return a, c, &output
}

func TestUserMessageActionsAndInlineEdit(t *testing.T) {
	a, c, output := messageEditFixture(t)
	view := a.composerView()
	validateBindings(t, view)
	for _, want := range []string{`class="codex-message-time"`, `>10:06</span>`, `data-pencode-copy="` + html.EscapeString(c.items[1].text.String()) + `"`, `data-ref="edit-message/target&#34;"`} {
		if !strings.Contains(view.HTML, want) {
			t.Fatalf("missing message action %q", want)
		}
	}
	if err := a.editMessage(c.items[1].data.ID); err != nil {
		t.Fatal(err)
	}
	view = a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, `data-ref="message-edit-text"`) || !strings.Contains(view.HTML, `data-pencode-submit-on-enter="message-edit-form"`) || strings.Contains(view.HTML, `data-ref="copy-message/target&#34;"`) {
		t.Fatal("editor did not replace the selected bubble")
	}
	cmd := command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "input", Ref: "message-edit-text", Value: "<updated>\n任务"}}}
	if n := testing.AllocsPerRun(100, func() { _ = a.handleCommand(cmd) }); n != 0 {
		t.Fatalf("editing input allocated %g", n)
	}
	if c.editDraft != cmd.event.Event.Value || c.draft != "keep composer draft" || output.Len() != 0 {
		t.Fatal("typing changed the composer or sent a request")
	}
	if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "click", Ref: "message-edit-cancel"}}}); err != nil {
		t.Fatal(err)
	}
	if c.editing != nil || len(c.items) != 4 || c.items[1].text.String() != "original\n**任务**" {
		t.Fatal("cancel changed the original history")
	}
	c.busy = true
	_ = a.editMessage(c.items[1].data.ID)
	if c.editing != nil || !strings.Contains(a.composerView().HTML, `class="codex-chat codex-chat-running"`) {
		t.Fatal("running conversation allowed an edit")
	}
}

func TestEditedMessageRevertsOnceAndRetriesWithOriginalAttachments(t *testing.T) {
	a, c, output := messageEditFixture(t,
		envelope{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"data":[],"nextCursor":"next"}`)},
		envelope{ID: json.RawMessage(`2`), Result: json.RawMessage(`{"data":[{"turnId":"edit-turn","item":{"id":"target\"","type":"userMessage","content":[{"type":"text","text":"original"},{"type":"image","url":"https://example.com/original.png"},{"type":"skill","name":"review","path":"/skills/review"}]}}]}`)},
		envelope{ID: json.RawMessage(`3`), Result: json.RawMessage(`{"thread":{"id":"thread"},"turnsBackwardsCursor":"retained"}`)},
		envelope{ID: json.RawMessage(`4`), Result: json.RawMessage(`{"data":[{"id":"prior-turn","status":"completed","items":[{"id":"prior","type":"userMessage","text":"prior prompt"}]}],"nextCursor":"fresh-cursor"}`)},
		envelope{ID: json.RawMessage(`5`), Error: &rpcError{Code: -32000, Message: "retry"}},
		envelope{Method: "turn/started", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"replacement","status":"inProgress"}}`)},
		envelope{Method: "item/completed", Params: json.RawMessage(`{"threadId":"thread","item":{"id":"new-user","type":"userMessage","content":[{"type":"text","text":"new prompt"}]}}`)},
		envelope{ID: json.RawMessage(`6`), Result: json.RawMessage(`{"turn":{"id":"replacement","status":"inProgress"}}`)},
	)
	_ = a.editMessage(c.items[1].data.ID)
	c.editDraft = "new prompt"
	if err := a.sendEditedMessage(); err == nil || !strings.Contains(err.Error(), "retry") {
		t.Fatal("expected a failed first send", err)
	}
	if !c.editReverted || c.editing == nil || c.editDraft != "new prompt" || len(c.items) != 2 || c.items[0].data.ID != "prior" || c.cursor != "fresh-cursor" || c.busy {
		t.Fatal("failed send lost the inline draft or retained removed history")
	}
	validateBindings(t, a.composerView())
	if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "submit", Ref: "message-edit-form"}}}); err != nil {
		t.Fatal(err)
	}
	if c.editing != nil || len(c.items) != 2 || c.items[1].data.ID != "new-user" || c.draft != "keep composer draft" || !c.busy {
		t.Fatal("successful retry lost the replacement or composer draft")
	}
	decoder := json.NewDecoder(output)
	wantMethods := []string{"thread/items/list", "thread/items/list", "thread/revert", "thread/turns/list", "turn/start", "turn/start"}
	for _, method := range wantMethods {
		var req struct {
			Method string
			Params struct {
				ThreadID, TurnID, BeforeTurnID, Cursor string
				Input                                  []input
			}
		}
		if err := decoder.Decode(&req); err != nil || req.Method != method || req.Params.ThreadID != c.ID {
			t.Fatal("unexpected request order", req, err)
		}
		if method == "thread/revert" && req.Params.BeforeTurnID != "edit-turn" {
			t.Fatal("reverted from the wrong turn", req)
		}
		if method == "turn/start" && (len(req.Params.Input) != 3 || req.Params.Input[0].Text != "new prompt" || req.Params.Input[1].URL != "https://example.com/original.png" || req.Params.Input[2].Path != "/skills/review") {
			t.Fatal("edited send lost original attachments", req)
		}
	}
}

func TestEditRevertFailurePreservesHistory(t *testing.T) {
	a, c, _ := messageEditFixture(t,
		envelope{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"data":[{"item":{"id":"target\"","type":"userMessage","content":[{"type":"text","text":"original"}]}}]}`)},
		envelope{ID: json.RawMessage(`2`), Error: &rpcError{Code: -32000, Message: "revert failed"}},
	)
	_ = a.editMessage(c.items[1].data.ID)
	c.editDraft = "new prompt"
	c.busy = true
	if err := a.sendEditedMessage(); err == nil {
		t.Fatal("submitted while running")
	}
	c.busy = false
	if err := a.sendEditedMessage(); err == nil || !strings.Contains(err.Error(), "revert failed") {
		t.Fatal(err)
	}
	if len(c.items) != 4 || c.editing == nil || c.editReverted || c.editDraft != "new prompt" {
		t.Fatal("failed revert changed local history")
	}
}

func TestMessageTimestampUsesRecordedTime(t *testing.T) {
	a, c, _ := messageEditFixture(t)
	i := c.items[1]
	stamp := time.Date(2026, 11, 4, 10, 8, 0, 0, time.Local)
	params, _ := json.Marshal(map[string]any{"threadId": c.ID, "startedAtMs": stamp.UnixMilli(), "item": threadItem{ID: i.data.ID, Type: "userMessage", Text: "任务"}})
	a.onMessage(envelope{Method: "item/started", Params: params})
	if !i.sentAt.Equal(stamp) || !strings.Contains(a.composerView().HTML, `>10:08</span>`) {
		t.Fatal("live message ignored the server timestamp")
	}
	i.replace(threadItem{ID: i.data.ID, Type: "userMessage", StartedAtMs: stamp.Add(time.Minute).UnixMilli(), Text: "任务"}, true)
	if !strings.Contains(a.composerView().HTML, `>10:09</span>`) {
		t.Fatal("history message ignored its recorded timestamp")
	}
}

func TestInlineEditSurvivesResumeAndOlderHistoryPages(t *testing.T) {
	a, c, _ := messageEditFixture(t,
		envelope{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"thread":{"id":"thread","name":"task","status":{"type":"idle"}}}`)},
		envelope{ID: json.RawMessage(`2`), Result: json.RawMessage(`{"data":[{"id":"later-turn","status":"completed","items":[{"id":"later","type":"userMessage","text":"later prompt"}]}],"nextCursor":"older-page"}`)},
		envelope{ID: json.RawMessage(`3`), Result: json.RawMessage(`{"data":[{"id":"edit-turn","status":"completed","items":[{"id":"target\"","type":"userMessage","text":"original"}]}]}`)},
	)
	_ = a.editMessage(c.items[1].data.ID)
	c.editDraft, c.loaded = "unsent edit", false
	previous := c.editing
	if err := a.resume(c.ID); err != nil {
		t.Fatal(err)
	}
	if c.editing == previous || c.editing != c.items[0] || c.editDraft != "unsent edit" || c.draft != "keep composer draft" || !strings.Contains(a.composerView().HTML, `>unsent edit</textarea>`) {
		t.Fatal("resuming lost the editor or its draft")
	}
}
