package main

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/pencode-labs/pencode/peb"
)

type uiPublisher interface {
	Show(peb.UI) error
	CloseUI(string) error
}

// Each tab owns its actor and app-server so a new conversation cannot replace
// another tab's draft, transcript, approvals or running turn.
type workspaceTab struct {
	*agent
	cancel context.CancelFunc
	open   bool
}

type workspaceHost struct {
	ctx            context.Context
	publisher      *peb.ActionPublisher
	settings       peb.SettingValues
	tabs           map[string]*workspaceTab
	active, closed *workspaceTab
	next           uint64
	workers        sync.WaitGroup
}

func (h *workspaceHost) configure(values peb.SettingValues) error {
	h.settings = values
	for _, tab := range h.tabs {
		if err := tab.submit(command{settings: values}); err != nil {
			return err
		}
	}
	return nil
}

func (h *workspaceHost) newTab(ctx peb.ActionContext) error {
	prefix := ""
	if h.next != 0 {
		prefix = strconv.FormatUint(h.next, 10) + "/"
	}
	h.next++
	actorCtx, cancel := context.WithCancel(h.ctx)
	tab := &workspaceTab{agent: &agent{
		ctx: actorCtx, publisher: &tabPublisher{publisher: h.publisher, prefix: prefix, ids: make(map[string]string)},
		commands: make(chan command, 64), done: make(chan struct{}),
	}, cancel: cancel, open: true}
	h.tabs[prefix], h.active = tab, tab
	h.workers.Go(tab.run)
	if h.settings != nil {
		if err := tab.submit(command{settings: h.settings}); err != nil {
			return err
		}
	}
	return tab.submit(command{action: true, context: ctx})
}

func (h *workspaceHost) open(ctx peb.ActionContext) error {
	if h.active == nil {
		return h.newTab(ctx)
	}
	h.active.open = true
	if h.closed == h.active {
		h.closed = nil
	}
	return h.active.submit(command{action: true, context: ctx})
}

func (h *workspaceHost) handleEvent(e peb.UIEvent) error {
	prefix := ""
	if scope, local, ok := strings.Cut(e.ID, "/"); ok {
		if _, err := strconv.ParseUint(scope, 10, 64); err == nil {
			prefix, e.ID = e.ID[:len(scope)+1], local
		}
	}
	tab := h.tabs[prefix]
	if tab == nil {
		return nil
	}
	if e.ID == "composer" {
		switch e.Event.Kind {
		case "click":
			if e.Event.Ref == "new" {
				if err := tab.submit(command{event: peb.UIEvent{ID: "composer", Event: peb.EditorEvent{Kind: "visibilitychange", Value: "hidden"}}}); err != nil {
					return err
				}
				return h.newTab(e.Context)
			}
		case "visibilitychange":
			if e.Event.Value == "visible" {
				h.active = tab
			}
		case "close":
			tab.open = false
			// Keep only the most recently closed workspace for reopening drafts.
			if h.closed != nil && h.closed != tab {
				h.closed.cancel()
				for id, previous := range h.tabs {
					if previous == h.closed {
						delete(h.tabs, id)
					}
				}
			}
			h.closed = tab
			if h.active == nil || !h.active.open {
				h.active = tab
			}
		}
	}
	return tab.submit(command{event: e})
}

func (h *workspaceHost) close() {
	for _, tab := range h.tabs {
		tab.cancel()
	}
	h.workers.Wait()
}

// Cache scoped IDs once; streaming updates reuse them without allocations.
type tabPublisher struct {
	publisher uiPublisher
	prefix    string
	ids       map[string]string
}

func (p *tabPublisher) id(local string) string {
	if p.prefix == "" {
		return local
	}
	if id, ok := p.ids[local]; ok {
		return id
	}
	id := p.prefix + local
	p.ids[local] = id
	return id
}

func (p *tabPublisher) Show(ui peb.UI) error {
	ui.ID = p.id(ui.ID)
	if anchor, ok := strings.CutPrefix(ui.Anchor, "ui:"); ok {
		id, ref, _ := strings.Cut(anchor, "#")
		ui.Anchor = "ui:" + p.id(id) + "#" + ref
	}
	return p.publisher.Show(ui)
}

func (p *tabPublisher) CloseUI(id string) error { return p.publisher.CloseUI(p.id(id)) }
