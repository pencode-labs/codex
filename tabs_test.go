package main

import (
	"testing"

	"github.com/pencode-labs/pencode/peb"
)

type capturedUI struct{ ui peb.UI }

func (p *capturedUI) Show(ui peb.UI) error { p.ui = ui; return nil }
func (p *capturedUI) CloseUI(id string) error {
	return p.Show(peb.UI{ID: id, Close: true})
}

func TestWorkspaceNewSessionStaysInActor(t *testing.T) {
	a := &agent{commands: make(chan command, 2)}
	h := workspaceHost{actor: a}
	event := peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "click", Ref: "new"}}
	if err := h.handleEvent(event); err != nil {
		t.Fatal(err)
	}
	if h.actor != a || (<-a.commands).event != event {
		t.Fatal("new session replaced the workspace actor")
	}
}
