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

func TestTabPublisherScopesSurfacesAndAnchors(t *testing.T) {
	var capture capturedUI
	p := tabPublisher{publisher: &capture, prefix: "2/", ids: make(map[string]string)}
	if err := p.Show(peb.UI{ID: "models", Anchor: "ui:composer#model-settings"}); err != nil {
		t.Fatal(err)
	}
	if capture.ui.ID != "2/models" || capture.ui.Anchor != "ui:2/composer#model-settings" {
		t.Fatal("menu escaped its tab", capture.ui)
	}
	if err := p.CloseUI("models"); err != nil || !capture.ui.Close || capture.ui.ID != "2/models" {
		t.Fatal("closing menu escaped its tab", capture.ui, err)
	}
	_ = p.Show(peb.UI{ID: "composer"})
	if allocations := testing.AllocsPerRun(100, func() { _ = p.Show(peb.UI{ID: "composer", Busy: true}) }); allocations != 0 {
		t.Fatalf("scoping streaming updates allocated %g times", allocations)
	}
}

func TestWorkspaceRoutesEventsToOwningTab(t *testing.T) {
	first := &workspaceTab{agent: &agent{commands: make(chan command, 2)}, open: true}
	second := &workspaceTab{agent: &agent{commands: make(chan command, 2)}, open: true}
	h := workspaceHost{tabs: map[string]*workspaceTab{"": first, "1/": second}, active: first}
	for _, event := range []peb.UIEvent{
		{ID: "1/composer", Event: peb.EditorEvent{Kind: "visibilitychange", Value: "visible"}},
		{ID: "1/models", Event: peb.EditorEvent{Kind: "click", Ref: "model/test"}},
	} {
		if err := h.handleEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if h.active != second || len(first.commands) != 0 {
		t.Fatal("events reached the wrong tab")
	}
	if got := <-second.commands; got.event.ID != "composer" {
		t.Fatal("composer ID was not restored", got)
	}
	if got := <-second.commands; got.event.ID != "models" || got.event.Event.Ref != "model/test" {
		t.Fatal("model event was changed or lost", got)
	}
	if err := h.handleEvent(peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "close"}}); err != nil {
		t.Fatal(err)
	}
	if h.active != second || !second.open || first.open {
		t.Fatal("closing another tab displaced the active session")
	}
}
