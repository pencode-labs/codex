package main

import (
	"encoding/json"
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pencode-labs/pencode/peb"
)

// Called only when building a user message's cached HTML.
func renderQuestionReplies(b *strings.Builder, text string) bool {
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
		b.WriteString(`<div class="codex-question-reply"><div class="codex-question-summary" title="`)
		b.WriteString(html.EscapeString(reply.Question))
		b.WriteString(`">`)
		b.WriteString(html.EscapeString(reply.Question))
		b.WriteString(`</div><div class="codex-text" data-pencode-preserve-whitespace>`)
		b.WriteString(html.EscapeString(reply.Answer))
		b.WriteString(`</div></div>`)
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

func (i *chatItem) renderMessageActions(b *strings.Builder) {
	b.WriteString(`<div class="codex-message-actions">`)
	if !i.sentAt.IsZero() {
		b.WriteString(`<span class="codex-message-time" title="`)
		b.WriteString(i.sentAt.Local().Format("2006-01-02 15:04:05"))
		b.WriteString(`">`)
		b.WriteString(i.sentAt.Local().Format("15:04"))
		b.WriteString(`</span>`)
	}
	if i.data.ID != "" {
		b.WriteString(`<button type="button" class="codex-icon-button" data-ref="copy-message/`)
		b.WriteString(html.EscapeString(i.data.ID))
		b.WriteString(`" data-pencode-copy="`)
		b.WriteString(html.EscapeString(i.text.String()))
		b.WriteString(`" title="复制消息" aria-label="复制消息"><img data-pencode-icon="copy" data-ref="copy-message-icon/`)
		b.WriteString(html.EscapeString(i.data.ID))
		b.WriteString(`" src="resources/icons/copy.svg" alt=""></button>`)
		if i.files != nil && i.files.id != "" {
			ref := "edit-message/" + i.data.ID
			b.WriteString(`<button type="button" class="codex-icon-button codex-message-edit" data-ref="`)
			b.WriteString(html.EscapeString(ref))
			b.WriteString(`" title="编辑消息" aria-label="编辑消息"><img src="resources/icons/pencil.svg" alt=""></button>`)
			i.linkBindings = append(i.linkBindings, peb.EditorBinding{Ref: ref, Kind: "click"})
		}
	}
	b.WriteString(`</div>`)
}

func (c *conversation) renderMessageEditor(b *strings.Builder, bindings []peb.EditorBinding) []peb.EditorBinding {
	b.WriteString(`<section class="codex-message codex-user codex-user-editing" aria-label="编辑消息" data-ref="message/`)
	b.WriteString(html.EscapeString(c.editing.data.ID))
	b.WriteString(`" data-pencode-region><form class="codex-user-bubble" data-ref="message-edit-form" data-pencode-focus="message-edit-text"><textarea class="codex-message-editor" data-ref="message-edit-text" data-pencode-autosize data-pencode-submit-on-enter="message-edit-form" data-pencode-caret="`)
	b.WriteString(strconv.Itoa(utf8.RuneCountInString(c.editing.text.String())))
	b.WriteByte(':')
	b.WriteString(strconv.FormatUint(c.editRevision, 10))
	b.WriteString(`" rows="2" aria-label="编辑消息"`)
	if c.busy {
		b.WriteString(` disabled`)
	}
	b.WriteString(`>`)
	b.WriteString(html.EscapeString(c.editDraft))
	b.WriteString(`</textarea><div class="codex-message-edit-footer"><button type="button" data-ref="message-edit-cancel"`)
	if c.busy {
		b.WriteString(` disabled`)
	}
	b.WriteString(`>取消</button><button type="submit" class="codex-message-edit-send"`)
	if c.busy {
		b.WriteString(` disabled`)
	}
	b.WriteString(`>发送</button></div></form></section>`)
	return append(bindings, peb.EditorBinding{Ref: "message-edit-text", Kind: "input"}, peb.EditorBinding{Ref: "message-edit-form", Kind: "submit"}, peb.EditorBinding{Ref: "message-edit-cancel", Kind: "click"})
}

func (a *agent) editMessage(id string) error {
	c := a.selected
	if c == nil || c.busy {
		return nil
	}
	for _, i := range c.items {
		if i.data.ID == id && i.data.Type == "userMessage" && i.files != nil && i.files.id != "" {
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
		clear(c.items[start:])
		c.items = append(c.items[:start], i)
		i.files, i.progress, i.html = nil, nil, ""
		c.progress, c.files, c.requests, c.turnID = nil, nil, nil, ""
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
