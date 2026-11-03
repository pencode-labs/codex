package main

import (
	"fmt"

	"github.com/pencode-labs/pencode/peb"
)

func (a *agent) requestCloseWorkspace() error { return a.publisher.CloseUI("composer") }

func (a *agent) markViewed(c *conversation) {
	if c != nil && c.unread {
		c.unread, a.historyDirty = false, true
	}
}

func (a *agent) setBusy(c *conversation, busy bool) {
	if c.busy != busy {
		a.historyDirty = true
	}
	if busy {
		c.unread = false
	}
	c.busy, c.dirty = busy, true
}

func (a *agent) handleLifecycle(event peb.LifecycleEvent) peb.LifecycleReply {
	if event.Kind == peb.ProjectClosed {
		for id, c := range a.conversations {
			if c.context.ProjectPath != event.ProjectPath {
				continue
			}
			if c.titleJob != nil {
				c.titleJob.cancel()
			}
			if a.selected == c {
				a.selectConversation(nil)
			}
			delete(a.conversations, id)
		}
		delete(a.drafts, event.ProjectPath)
		for scope := range a.attachments {
			if scope.project == event.ProjectPath {
				delete(a.attachments, scope)
			}
		}
		for scope := range a.skills {
			if scope.project == event.ProjectPath {
				delete(a.skills, scope)
			}
		}
		if len(a.conversations) == 0 {
			a.closeClient()
		}
		a.historyDirty = true
		return peb.LifecycleReply{}
	}
	running := 0
	for _, c := range a.conversations {
		if c.busy && (event.Kind == peb.WindowClosing || c.context.ProjectPath == event.ProjectPath) {
			running++
		}
	}
	if running == 0 {
		return peb.LifecycleReply{}
	}
	return peb.LifecycleReply{Message: fmt.Sprintf("Codex 仍有 %d 个会话正在运行（包括后台会话）。", running)}
}
