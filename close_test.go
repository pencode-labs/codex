package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pencode-labs/pencode/peb"
)

func TestCloseWorkspaceInterruptsAfterPendingTurnStart(t *testing.T) {
	var output bytes.Buffer
	c := &conversation{thread: thread{ID: "thread"}, busy: true, loaded: true}
	a := &agent{selected: c, workspaceOpen: true, commands: make(chan command, 1), conversations: map[string]*conversation{c.ID: c}}
	client := &rpcClient{ctx: t.Context(), writer: json.NewEncoder(&output), messages: make(chan envelope, 2), commands: a.commands, handle: a.onMessage}
	a.client = client
	client.dispatch = func(cmd command) {
		a.handlePendingCommand(cmd)
		client.messages <- envelope{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"turn":{"id":"turn","status":"inProgress"}}`)}
	}
	a.commands <- command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "close"}, Reason: "button"}}
	result, err := call[struct {
		Turn turn `json:"turn"`
	}](client, "turn/start", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if a.workspaceOpen || a.selected != nil || !a.exitsPending || !c.exitRequested || strings.Contains(output.String(), "turn/interrupt") {
		t.Fatal("closing must exit immediately and defer the interrupt until turn/start returns", output.String())
	}
	c.turnID = result.Turn.ID
	client.messages <- envelope{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"interrupted"}}`)}
	client.messages <- envelope{ID: json.RawMessage(`2`), Result: json.RawMessage(`{}`)}
	a.exitConversations()
	if !strings.Contains(output.String(), `"method":"turn/interrupt"`) || !strings.Contains(output.String(), `"turnId":"turn"`) || c.busy || c.loaded || a.exitsPending || c.exitRequested {
		t.Fatal("close did not interrupt the right turn or release the conversation", output.String())
	}
	if a.conversations[c.ID] != c {
		t.Fatal("closing deleted conversation history")
	}
}

func TestCloseWorkspaceLeavesIdleConversationInHistory(t *testing.T) {
	c := &conversation{thread: thread{ID: "thread"}, loaded: true, items: []*chatItem{newChatItem(threadItem{Type: "agentMessage", Text: "reply"}, true)}}
	a := &agent{selected: c, workspaceOpen: true, conversations: map[string]*conversation{c.ID: c}}
	if err := a.closeWorkspace(); err != nil {
		t.Fatal(err)
	}
	if a.selected != nil || a.workspaceOpen || c.loaded || c.items != nil || a.exitsPending || a.conversations[c.ID] != c {
		t.Fatal("closing an idle conversation must exit while keeping history")
	}
}
