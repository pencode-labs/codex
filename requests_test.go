package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pencode-labs/pencode/peb"
)

func TestServerRequestsKeepWireIDsAndScopePermissions(t *testing.T) {
	var output bytes.Buffer
	c := &conversation{thread: thread{ID: "thread"}}
	a := &agent{client: &rpcClient{writer: json.NewEncoder(&output)}, selected: c, conversations: map[string]*conversation{c.ID: c}}
	request := func(id, method, params string) {
		t.Helper()
		output.Reset()
		a.onMessage(envelope{ID: json.RawMessage(id), Method: method, Params: json.RawMessage(params)})
		if len(c.requests) != 1 {
			t.Fatal("request not retained")
		}
		validateBindings(t, a.composerView())
	}
	request(`"command-id"`, "item/commandExecution/requestApproval", `{"threadId":"thread","command":"test"}`)
	if err := a.answer(c, `"command-id"`, "decline"); err != nil {
		t.Fatal(err)
	}
	var result struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(output.Bytes(), &result) != nil || string(result.ID) != `"command-id"` || string(result.Result) != `{"decision":"decline"}` {
		t.Fatal(output.String())
	}
	request(`2`, "item/permissions/requestApproval", `{"threadId":"thread","permissions":{"network":{"enabled":true}}}`)
	if err := a.answer(c, "2", "accept"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"permissions":{"network":{"enabled":true}}`) || !strings.Contains(output.String(), `"scope":"turn"`) {
		t.Fatal(output.String())
	}
	request(`3`, "item/permissions/requestApproval", `{"threadId":"thread","permissions":{"network":{"enabled":true}}}`)
	if err := a.answer(c, "3", "decline"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"permissions":{}`) {
		t.Fatal("denied permission leaked into grant", output.String())
	}
	request(`4`, "item/tool/requestUserInput", `{"threadId":"thread","questions":[{"id":"choice","question":"Which option?","options":[{"label":"A","description":"First"}]}]}`)
	if err := a.answer(c, "4", "answer"); err == nil || output.Len() != 0 {
		t.Fatal("empty answer was submitted")
	}
	if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "input", Ref: "answer/4/choice", Value: "A"}}}); err != nil {
		t.Fatal(err)
	}
	if err := a.answer(c, "4", "answer"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"choice":{"answers":["A"]}`) {
		t.Fatal(output.String())
	}
	output.Reset()
	a.onMessage(envelope{ID: json.RawMessage(`5`), Method: "unsupported/request", Params: json.RawMessage(`{"threadId":"thread"}`)})
	if !strings.Contains(output.String(), `"code":-32601`) {
		t.Fatal("unsupported request left the server hanging")
	}
}

func TestRPCDispatchesNotificationsAndRejectsWrongIDs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	seen := 0
	c := &rpcClient{ctx: ctx, cancel: cancel, writer: json.NewEncoder(&output), messages: make(chan envelope, 3), handle: func(envelope) { seen++ }}
	c.messages <- envelope{Method: "event", Params: json.RawMessage(`{}`)}
	c.messages <- envelope{ID: json.RawMessage(`1`), Result: json.RawMessage(`{"ok":true}`)}
	value, err := call[struct {
		OK bool `json:"ok"`
	}](c, "request", struct{}{})
	if err != nil || !value.OK || seen != 1 {
		t.Fatal(value, err, seen)
	}
	c.messages <- envelope{ID: json.RawMessage(`99`), Result: json.RawMessage(`{}`)}
	if _, err = call[json.RawMessage](c, "request", struct{}{}); err == nil {
		t.Fatal("uncorrelated response accepted")
	}
}

func TestWorkspaceDraftsAreIndependent(t *testing.T) {
	a := &agent{drafts: make(map[string]string), conversations: make(map[string]*conversation)}
	input := func(root, text string) {
		a.context.ProjectPath = root
		if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "input", Ref: "prompt", Value: text}}}); err != nil {
			t.Fatal(err)
		}
	}
	input("project-one", "first draft")
	input("project-two", "second draft")
	a.context.ProjectPath = "project-one"
	view := a.composerView()
	if !strings.Contains(view.HTML, "first draft") || strings.Contains(view.HTML, "second draft") {
		t.Fatal("composer mixed workspace drafts")
	}
}
