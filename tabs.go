package main

import (
	"context"
	"sync"

	"github.com/pencode-labs/pencode/peb"
)

type uiPublisher interface {
	Show(peb.UI) error
	CloseUI(string) error
}

// One actor owns the workspace and all foreground and background conversations.
type workspaceHost struct {
	ctx       context.Context
	publisher *peb.ActionPublisher
	settings  peb.SettingValues
	actor     *agent
	cancel    context.CancelFunc
	workers   sync.WaitGroup
}

func (h *workspaceHost) configure(values peb.SettingValues) error {
	h.settings = values
	if h.actor != nil {
		return h.actor.submit(command{settings: values})
	}
	return nil
}

func (h *workspaceHost) open(ctx peb.ActionContext) error {
	if h.actor == nil {
		actorCtx, cancel := context.WithCancel(h.ctx)
		h.cancel = cancel
		h.actor = &agent{ctx: actorCtx, publisher: h.publisher, commands: make(chan command, 64), done: make(chan struct{})}
		h.workers.Go(h.actor.run)
		if h.settings != nil {
			if err := h.actor.submit(command{settings: h.settings}); err != nil {
				return err
			}
		}
	}
	return h.actor.submit(command{action: true, context: ctx})
}

func (h *workspaceHost) handleEvent(e peb.UIEvent) error {
	if h.actor == nil {
		return nil
	}
	return h.actor.submit(command{event: e})
}

func (h *workspaceHost) lifecycle(event peb.LifecycleEvent) (peb.LifecycleReply, error) {
	if h.actor == nil {
		return peb.LifecycleReply{}, nil
	}
	reply := make(chan peb.LifecycleReply, 1)
	if err := h.actor.submit(command{lifecycle: &event, reply: reply}); err != nil {
		return peb.LifecycleReply{}, err
	}
	select {
	case result := <-reply:
		return result, nil
	case <-h.actor.done:
		return peb.LifecycleReply{}, context.Canceled
	}
}

func (h *workspaceHost) close() {
	if h.cancel != nil {
		h.cancel()
	}
	h.workers.Wait()
}
