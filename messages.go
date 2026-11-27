package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/onix-engine/onix/html/markup"
	"github.com/pencode-labs/pencode/peb"
)

// Called only when building a user message's cached HTML.
func renderQuestionReplies(b *markup.Builder, text string) bool {
	payload, ok := strings.CutPrefix(strings.TrimSpace(text), "<send_user_message_question_reply>")
	if !ok {
		return false
	}
	payload, ok = strings.CutSuffix(payload, "</send_user_message_question_reply>")
	if !ok {
		return false
	}
	var replies []struct {
		Question string `json:"question"`
		Answer   string `json:"answer"`
	}
	if json.Unmarshal([]byte(payload), &replies) != nil || len(replies) == 0 {
		return false
	}
	for _, reply := range replies {
		if reply.Question == "" || reply.Answer == "" {
			return false
		}
	}
	for _, reply := range replies {
		b.Open("div", "class", "codex-question-reply")
		b.TextElement("div", reply.Question, "class", "codex-question-summary", "title", reply.Question)
		b.TextElement("div", reply.Answer, "class", "codex-text", "data-pencode-preserve-whitespace", "")
		b.End()
	}
	return true
}

func (i *chatItem) setSentAt(started int64) {
	if i.data.Type != "userMessage" || !i.sentAt.IsZero() {
		return
	}
	if started > 0 {
		i.sentAt = time.Unix(started, 0)
	} else if i.progress != nil {
		i.sentAt = i.progress.started
	}
}

func (c *conversation) removePendingMessage(i *chatItem) {
	if index := slices.Index(c.items, i); index >= 0 {
		c.items = slices.Delete(c.items, index, index+1)
		delete(c.itemsByID, i.data.ID)
		c.dirty = true
	}
}

func (c *conversation) clearServerHistory() {
	c.items = slices.DeleteFunc(c.items, func(i *chatItem) bool {
		if i.sendError == "" {
			return true
		}
		i.progress, i.files = nil, nil
		return false
	})
	if len(c.items) == 0 {
		c.items = nil
	}
	c.itemsByID = nil
}

func (c *conversation) confirmMessage(item threadItem) {
	byID := c.itemIndex()
	if i := byID[item.ClientID]; i != nil && i.pending {
		if existing := byID[item.ID]; existing != nil && existing != i {
			c.removePendingMessage(i)
			i.pending = false
			return
		}
		delete(byID, item.ClientID)
		i.data.ID = item.ID
		byID[item.ID] = i
	}
}

func (i *chatItem) renderMessageActions(b *markup.Builder) {
	b.Open("div", "class", "codex-message-actions")
	if !i.sentAt.IsZero() {
		b.TextElement("span", i.sentAt.Local().Format("15:04"), "class", "codex-message-time", "title", i.sentAt.Local().Format("2006-01-02 15:04:05"))
	}
	if i.data.ID != "" {
		b.Open("button", "type", "button", "class", "codex-icon-button", "data-ref", "copy-message/"+i.data.ID, "data-pencode-copy", i.text.String(), "title", "复制消息", "aria-label", "复制消息")
		b.Element("img", "data-pencode-icon", "copy", "data-ref", "copy-message-icon/"+i.data.ID, "src", "resources/icons/copy.svg", "alt", "")
		b.End()
		if !i.pending && i.files != nil && i.files.id != "" {
			ref := "edit-message/" + i.data.ID
			b.Open("button", "type", "button", "class", "codex-icon-button codex-message-edit", "data-ref", ref, "title", "编辑消息", "aria-label", "编辑消息")
			b.Element("img", "src", "resources/icons/pencil.svg", "alt", "")
			b.End()
			i.linkBindings = append(i.linkBindings, peb.EditorBinding{Ref: ref, Kind: "click"})
		}
	}
	b.End()
}

func (c *conversation) renderMessageEditor(b *markup.Builder, bindings []peb.EditorBinding) []peb.EditorBinding {
	b.Open("section", "class", "codex-message codex-user codex-user-editing", "aria-label", "编辑消息", "data-ref", "message/"+c.editing.data.ID, "data-pencode-region", "")
	b.Open("form", "class", "codex-user-bubble", "data-ref", "message-edit-form", "data-pencode-focus", "message-edit-text")
	field := b.TextElement("textarea", c.editDraft, "class", "codex-message-editor", "data-ref", "message-edit-text", "data-pencode-autosize", "", "data-pencode-submit-on-enter", "message-edit-form", "data-pencode-caret", strconv.Itoa(utf8.RuneCountInString(c.editing.text.String()))+":"+strconv.FormatUint(c.editRevision, 10), "rows", "2", "aria-label", "编辑消息")
	b.Open("div", "class", "codex-message-edit-footer")
	cancel := b.TextElement("button", "取消", "type", "button", "data-ref", "message-edit-cancel")
	send := b.TextElement("button", "发送", "type", "submit", "class", "codex-message-edit-send")
	if c.busy {
		field.SetAttr("disabled", "")
		cancel.SetAttr("disabled", "")
		send.SetAttr("disabled", "")
	}
	b.End()
	b.End()
	b.End()
	return append(bindings, peb.EditorBinding{Ref: "message-edit-text", Kind: "input"}, peb.EditorBinding{Ref: "message-edit-form", Kind: "submit"}, peb.EditorBinding{Ref: "message-edit-cancel", Kind: "click"})
}

func (a *agent) editMessage(id string) error {
	c := a.selected
	if c == nil || c.busy {
		return nil
	}
	for _, i := range c.items {
		if i.data.ID == id && i.data.Type == "userMessage" && !i.pending && i.files != nil && i.files.id != "" {
			c.cancelEdit()
			c.editing, c.editDraft = i, i.text.String()
			c.editRevision++
			c.dirty = true
			a.dismissCompletion()
			return nil
		}
	}
	return nil
}

func (c *conversation) cancelEdit() {
	if c.editing == nil {
		return
	}
	if c.editReverted {
		c.items = slices.DeleteFunc(c.items, func(i *chatItem) bool { return i == c.editing })
		c.itemsByID = nil
	}
	c.editing, c.editDraft, c.editInputs, c.editReverted = nil, "", nil, false
	c.dirty = true
}

// Read the durable input before reverting so images and skill references retain
// their original sources without keeping large image payloads in display items.
func (a *agent) editedInputs(c *conversation, turnID, itemID string) ([]input, error) {
	var cursor string
	for {
		page, err := call[struct {
			NextCursor string `json:"nextCursor"`
			Data       []struct {
				Item threadItem `json:"item"`
			} `json:"data"`
		}](a.client, "thread/items/list", struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Cursor   string `json:"cursor,omitempty"`
			Limit    int    `json:"limit"`
		}{c.ID, turnID, cursor, 100})
		if err != nil {
			return nil, err
		}
		for _, entry := range page.Data {
			item := entry.Item
			if item.ID != itemID || item.Type != "userMessage" {
				continue
			}
			inputs := make([]input, 1, len(item.Content)+1)
			inputs[0] = input{Type: "text", Text: c.editDraft}
			for _, part := range item.Content {
				if part.Type != "text" {
					inputs = append(inputs, part)
				}
			}
			return inputs, nil
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			return nil, fmt.Errorf("未找到原消息，请刷新会话后重试")
		}
		cursor = page.NextCursor
	}
}

func (a *agent) sendEditedMessage() error {
	c := a.selected
	if c == nil || c.editing == nil {
		return nil
	}
	if c.busy {
		return fmt.Errorf("当前会话正在工作，请等待完成或点击停止")
	}
	if strings.TrimSpace(c.editDraft) == "" && len(c.editing.images) == 0 {
		return fmt.Errorf("消息不能为空")
	}
	if err := a.connect(); err != nil {
		return err
	}
	if !c.loaded {
		if err := a.resume(c.ID); err != nil {
			return err
		}
		if !c.loaded {
			return fmt.Errorf("%s", c.status)
		}
	}
	if c.busy {
		return fmt.Errorf("当前会话正在工作，请等待完成或点击停止")
	}
	if !c.editReverted {
		i := c.editing
		if i.files == nil || i.files.id == "" {
			return fmt.Errorf("无法确定消息所属任务，请刷新会话后重试")
		}
		inputs, err := a.editedInputs(c, i.files.id, i.data.ID)
		if err != nil {
			return err
		}
		if c.busy {
			return fmt.Errorf("当前会话正在工作，请等待完成或点击停止")
		}
		result, err := call[struct {
			TurnsBackwardsCursor string `json:"turnsBackwardsCursor"`
		}](a.client, "thread/revert", struct {
			ThreadID     string `json:"threadId"`
			BeforeTurnID string `json:"beforeTurnId"`
		}{c.ID, i.files.id})
		if err != nil {
			return err
		}
		start := slices.IndexFunc(c.items, func(item *chatItem) bool { return item.files != nil && item.files.id == i.files.id })
		if start < 0 {
			start = len(c.items)
		}
		c.replaceItemTail(start, i)
		i.files, i.progress, i.html = nil, nil, ""
		c.progress, c.files, c.requests, c.turnID = nil, nil, nil, ""
		c.interrupted = false
		c.editInputs, c.editReverted, c.dirty = inputs, true, true
		c.turnRevision++
		// Revert replaces the history generation, invalidating old page cursors.
		c.cursor = result.TurnsBackwardsCursor
		if c.cursor != "" {
			if err := a.readHistory(c); err != nil {
				return err
			}
		}
	}
	c.editInputs[0].Text = c.editDraft
	if err := a.startTurn(c, c.editInputs); err != nil {
		c.status = "记录已回退，发送失败；可在原位置重试或取消。"
		return err
	}
	c.cancelEdit()
	return nil
}
