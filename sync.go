package main

import (
	"crypto/sha256"
	"encoding/json"
	"slices"
	"time"
)

const externalSessionStatus = "正在自动同步其他 Codex 客户端已保存的记录。停止任务请在原客户端操作。"

type turnPage struct {
	Data       []turn `json:"data"`
	NextCursor string `json:"nextCursor"`
}

func (a *agent) openConversation(id string) error {
	c := a.conversations[id]
	if c == nil || !c.observing || a.client == nil {
		return a.resume(id)
	}
	if err := a.setContext(c.context); err != nil {
		return err
	}
	a.showHistory = true
	a.selectConversation(c)
	return a.syncConversation(c)
}

func (a *agent) updateThreadStatus(id string, status threadStatus) {
	for index := range a.history.Data {
		if a.history.Data[index].ID == id {
			a.history.Data[index].Status = status
			a.historyDirty = true
			break
		}
	}
	if c := a.conversations[id]; c != nil && !c.observing {
		c.Status = status
		c.turnRevision++ // An older history response must not resurrect activity.
		a.setBusy(c, status.Type == "active")
	}
}

// Stored history represents an unfinished turn as interrupted when its writer
// belongs to another server. Explicit interruption records a completion time.
// This infers activity from persistence; it cannot detect a crashed writer.
func normalizeObservedTurn(t *turn) {
	if t.Status == "interrupted" && t.StartedAt > 0 && t.CompletedAt == 0 && t.DurationMs == nil && t.Error == nil {
		t.Status = "inProgress"
	}
}

func (a *agent) hasObservers() bool {
	if a.client != nil {
		for _, c := range a.conversations {
			if c.observing {
				return true
			}
		}
	}
	return false
}

func (a *agent) syncObserved() {
	now := time.Now()
	for _, c := range a.conversations {
		if !c.observing || now.Before(c.syncAfter) {
			continue
		}
		err := a.syncConversation(c)
		interval := 5 * time.Second
		if c.busy || c == a.selected && a.workspaceOpen && !a.workspaceHidden {
			interval = time.Second
		}
		if err != nil {
			c.syncError = "同步其他进程的会话失败，将自动重试：" + err.Error()
			c.dirty = true
			interval = 10 * time.Second
		}
		c.syncAfter = time.Now().Add(interval)
	}
}

func (a *agent) observedPage(id, cursor string) (json.RawMessage, error) {
	return call[json.RawMessage](a.client, "thread/turns/list", struct {
		ThreadID      string `json:"threadId"`
		Limit         int    `json:"limit"`
		SortDirection string `json:"sortDirection"`
		ItemsView     string `json:"itemsView"`
		Cursor        string `json:"cursor,omitempty"`
	}{id, 10, "desc", "full", cursor})
}

func (a *agent) syncConversation(c *conversation) error {
	raw, err := a.observedPage(c.ID, "")
	if err != nil {
		return err
	}
	if c.syncError != "" {
		c.syncError, c.dirty = "", true
	}
	digest := sha256.Sum256(raw)
	if digest == c.observedDigest {
		return nil
	}
	var page turnPage
	if err = json.Unmarshal(raw, &page); err != nil {
		return err
	}
	turns := page.Data
	// Catch up across several pages if the observer missed more than one page
	// of turns. Opaque cursors remain owned by app-server.
	for len(turns) > 0 && c.observedTurn != "" && page.NextCursor != "" && !slices.ContainsFunc(turns, func(t turn) bool { return t.ID == c.observedTurn }) {
		raw, err = a.observedPage(c.ID, page.NextCursor)
		if err != nil {
			return err
		}
		page = turnPage{}
		if err = json.Unmarshal(raw, &page); err != nil {
			return err
		}
		turns = append(turns, page.Data...)
	}
	if len(turns) > 0 {
		normalizeObservedTurn(&turns[0])
		a.reconcileObserved(c, turns)
	}
	c.observedDigest = digest
	return nil
}

// Pages arrive newest first. Keep the older prefix and reuse existing items so
// expansion state and references survive authoritative snapshot replacement.
func (a *agent) reconcileObserved(c *conversation, turns []turn) {
	latest := turns[0]
	old := c.items
	prefix := len(old)
	for index, i := range old {
		if i.files != nil && slices.ContainsFunc(turns, func(t turn) bool { return t.ID == i.files.id }) {
			prefix = index
			break
		}
	}
	var updated []*chatItem
	for index := len(turns) - 1; index >= 0; index-- {
		t := turns[index]
		files := newTurnFiles(t.ID, c.context.ProjectPath)
		previous := c.turnFiles(t.ID)
		if previous != nil {
			files.collapsed = previous.collapsed
		}
		progress := c.turnProgress(t.ID)
		if progress != nil || t.StartedAt > 0 || t.DurationMs != nil {
			if progress == nil {
				progress = &turnProgress{}
			}
			progress.update(t, time.Now())
		}
		for _, data := range t.Items {
			files.add(data)
			var item *chatItem
			for _, existing := range old {
				if existing.data.ID == data.ID {
					item = existing
					break
				}
			}
			if item == nil {
				item = &chatItem{}
			}
			item.replace(data, t.Status != "inProgress")
			item.files, item.progress = files, progress
			item.setSentAt(t.StartedAt)
			if progress != nil && item.modelText() {
				progress.received = true
			}
			updated = append(updated, item)
		}
		if previous != nil {
			for fi := range files.files {
				for _, before := range previous.files {
					if files.files[fi].path == before.path {
						files.files[fi].expanded = before.expanded
						break
					}
				}
			}
		}
		files.complete = t.Status != "inProgress"
		if index == 0 {
			c.files, c.progress = files, progress
		}
	}
	c.items = append(old[:prefix], updated...)
	changed := latest.ID != c.observedTurn || latest.Status != c.observedStatus
	if changed && latest.Status == "completed" && c.observedTurn != "" && (c != a.selected || !a.workspaceOpen || a.workspaceHidden) {
		c.unread, a.historyDirty = true, true
	}
	a.setBusy(c, latest.Status == "inProgress")
	c.turnID = ""
	c.status = externalSessionStatus
	if c.busy {
		c.turnID = latest.ID
	}
	if latest.Error != nil {
		c.status = latest.Error.Message
	}
	if latest.Status == "interrupted" {
		c.status = "任务已在原客户端停止。"
	}
	c.observedTurn, c.observedStatus = latest.ID, latest.Status
	c.dirty = true
}
