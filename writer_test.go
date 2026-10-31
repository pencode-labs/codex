package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pencode-labs/pencode/peb"
)

func TestOccupiedThreadOpensHistoryAndRetriesResume(t *testing.T) {
	fixtureExecutable(t)
	lock := filepath.Join(t.TempDir(), "writer.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PENCODE_CODEX_WRITER_LOCK", lock)
	t.Setenv("PENCODE_CODEX_TURN_PAGES", "1")
	t.Setenv("PENCODE_CODEX_TURN_CHANGES", "running")
	a := &agent{ctx: t.Context(), context: peb.ActionContext{ProjectPath: t.TempDir()}, conversations: make(map[string]*conversation), workspaceOpen: true}
	t.Cleanup(a.closeClient)
	if err := a.handleCommand(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "click", Ref: "history/saved"}}}); err != nil {
		t.Fatal("occupied history could not be opened", err)
	}
	c := a.selected
	if c == nil || c.ID != "saved" || c.Name != "Saved task" || c.loaded || c.busy || c.turnID != "" || c.status == "" || !a.history.contains(c.ID) {
		t.Fatal("occupied thread was not opened for viewing", c)
	}
	view := a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, "Stored conversation") || !strings.Contains(view.HTML, c.status) || strings.Contains(view.HTML, `data-ref="stop"`) || a.progressVisible() {
		t.Fatal("read-only history lost its content or exposed task controls", view.HTML)
	}
	if err := a.loadHistory(c); err != nil || !strings.Contains(a.composerView().HTML, "Older conversation") {
		t.Fatal("occupied history could not be paged", err)
	}
	c.draft = "continue"
	a.addAttachment("context.go")
	next := a.client.next
	if err := a.send(); err == nil || err.Error() != c.status {
		t.Fatal("sending to an occupied thread did not explain the conflict", err)
	}
	if c.draft != "continue" || len(a.attachments[a.attachmentScope()]) != 1 || c.loaded || c.busy || a.client.next != next+3 {
		t.Fatal("failed send lost its draft/attachment or submitted a turn")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := a.resume(c.ID); err != nil {
		t.Fatal("released thread could not be resumed", err)
	}
	if a.selected != c || !c.loaded || !c.busy || c.turnID != "old-turn" || c.status != "" || c.draft != "continue" || len(a.attachments[a.attachmentScope()]) != 1 {
		t.Fatal("released thread did not restore ownership and its draft", c)
	}
}

func TestResumeOnlyReadsHistoryForWriterConflicts(t *testing.T) {
	for _, rpcErr := range []rpcError{
		{Code: -32600, Message: "thread saved already has an active writer"},
		{Code: -32600, Message: "thread other already has an active writer"},
		{Code: -32000, Message: "thread saved already has an active writer"},
		{Code: -32600, Message: "thread not found"},
	} {
		t.Run(rpcErr.Error(), func(t *testing.T) {
			var output bytes.Buffer
			client := &rpcClient{ctx: t.Context(), writer: json.NewEncoder(&output), messages: make(chan envelope, 2)}
			client.messages <- envelope{ID: json.RawMessage(`1`), Error: &rpcErr}
			readErr := &rpcError{Code: -32000, Message: "cannot read history"}
			client.messages <- envelope{ID: json.RawMessage(`2`), Error: readErr}
			a := &agent{client: client, conversations: make(map[string]*conversation)}
			err := a.resume("saved")
			conflict := rpcErr.Code == -32600 && rpcErr.Message == "thread saved already has an active writer"
			want := &rpcErr
			if conflict {
				want = readErr
			}
			if err != want || strings.Contains(output.String(), `"method":"thread/read"`) != conflict || a.selected != nil || len(a.conversations) != 0 {
				t.Fatal("unexpected fallback or lost RPC error", err, output.String())
			}
		})
	}
}
