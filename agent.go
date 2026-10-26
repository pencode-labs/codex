package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/pencode-labs/pencode/peb"
)

var stderrWriter = os.Stderr

type input struct {
	Type         string     `json:"type"`
	Text         string     `json:"text"`
	TextElements []struct{} `json:"text_elements"`
}
type change struct {
	Path string `json:"path"`
	Diff string `json:"diff"`
}
type threadItem struct {
	ID               string   `json:"id"`
	Type             string   `json:"type"`
	Text             string   `json:"text"`
	Content          []input  `json:"content"`
	Summary          []string `json:"summary"`
	Command          string   `json:"command"`
	Status           string   `json:"status"`
	AggregatedOutput string   `json:"aggregatedOutput"`
	Changes          []change `json:"changes"`
	Server           string   `json:"server"`
	Tool             string   `json:"tool"`
	Query            string   `json:"query"`
}
type turn struct {
	ID     string       `json:"id"`
	Status string       `json:"status"`
	Items  []threadItem `json:"items"`
	Error  *rpcError    `json:"error"`
}
type thread struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Preview string `json:"preview"`
	Cwd     string `json:"cwd"`
}
type chatItem struct {
	data         threadItem
	text, output strings.Builder
	complete     bool
	html         string
}
type approval struct {
	id      json.RawMessage
	method  string
	params  json.RawMessage
	answers map[string]string
}
type conversation struct {
	thread
	context                   peb.ActionContext
	items                     []*chatItem
	requests                  []*approval
	draft, turnID, cursor     string
	status                    string
	turnRevision              uint64
	busy, open, dirty, loaded bool
}
type threadPage struct {
	Data       []thread `json:"data"`
	NextCursor string   `json:"nextCursor"`
}

type agent struct {
	ctx                                context.Context
	publisher                          *peb.ActionPublisher
	commands                           chan command
	done                               chan struct{}
	client                             *rpcClient
	context                            peb.ActionContext
	conversations                      map[string]*conversation
	selected                           *conversation
	drafts                             map[string]string
	model, status                      string
	history                            threadPage
	showHistory, panelOpen, panelDirty bool
	composerMenu, modelSettings        bool
	models                             []modelOption
	modelsLoaded                       bool
	modelError                         string
}

func (a *agent) run() {
	defer close(a.done)
	defer func() { a.client.close() }()
	a.conversations = make(map[string]*conversation)
	a.drafts = make(map[string]string)
	flush := time.NewTimer(time.Hour)
	flush.Stop()
	defer flush.Stop()
	var tick <-chan time.Time
	for {
		var messages <-chan envelope
		if a.client != nil {
			messages = a.client.messages
		}
		select {
		case <-a.ctx.Done():
			return
		case cmd := <-a.commands:
			if err := a.handleCommand(cmd); err != nil {
				a.status = err.Error()
				c := a.conversations[cmd.event.ID]
				if c == nil && cmd.event.ID == "composer" {
					c = a.selected
				}
				if c != nil {
					c.status = err.Error()
					c.dirty = true
				}
				if c != nil {
					a.status = ""
				}
				a.panelDirty = true
				fmt.Fprintln(stderrWriter, err)
			}
		case message, ok := <-messages:
			if !ok {
				a.disconnected()
				break
			}
			a.onMessage(message)
		case <-tick:
			a.flush()
			tick = nil
		}
		if tick == nil && a.dirty() {
			flush.Reset(80 * time.Millisecond)
			tick = flush.C
		}
	}
}

func (a *agent) dirty() bool {
	if a.panelDirty && a.panelOpen {
		return true
	}
	for _, c := range a.conversations {
		if c.dirty && c.open {
			return true
		}
	}
	return false
}

func (a *agent) disconnected() {
	a.client.close()
	err := a.client.err
	a.client = nil
	a.models, a.modelsLoaded = nil, false
	a.status = fmt.Sprintf("Codex 连接已断开：%v。重新发送或打开历史可重连。", err)
	for _, c := range a.conversations {
		c.loaded, c.busy = false, false
		c.turnID = ""
		c.requests = nil
		c.status = a.status
		c.dirty = true
	}
	a.panelDirty = true
}

func (a *agent) connect() error {
	if a.client != nil {
		if a.client.ctx.Err() == nil {
			return nil
		}
		a.disconnected()
	}
	root := a.context.ProjectPath
	if !filepath.IsAbs(root) {
		return fmt.Errorf("请先打开项目目录，再使用 Codex")
	}
	client, err := startClient(a.ctx, root, a.onMessage)
	if err != nil {
		return err
	}
	a.client = client
	_, err = call[json.RawMessage](client, "initialize", map[string]any{"clientInfo": map[string]string{"name": "pencode_codex", "title": "Pencode Codex Agent", "version": "0.1.0"}})
	if err == nil {
		err = client.notify("initialized", struct{}{})
	}
	if err != nil {
		client.close()
		a.client = nil
		return err
	}
	a.status = ""
	return nil
}

func (a *agent) handleCommand(cmd command) error {
	if cmd.action {
		if a.context.ProjectPath != cmd.context.ProjectPath {
			a.history = threadPage{}
			a.showHistory = false
			a.status = ""
		}
		a.context = cmd.context
		// Keep each project's composer independent when switching workspaces.
		if a.selected != nil && a.selected.Cwd != cmd.context.ProjectPath {
			a.selected = nil
		}
		a.panelOpen = !a.panelOpen
		if !a.panelOpen {
			if err := a.closeModels(); err != nil {
				return err
			}
			return a.publisher.CloseUI("composer")
		}
		a.panelDirty = true
		return nil
	}
	e := cmd.event
	if e.ID == "models" {
		return a.handleModelEvent(e.Event)
	}
	if e.ID == "composer" {
		switch e.Event.Kind {
		case "close":
			a.panelOpen = false
			return a.closeModels()
		case "visibilitychange":
			if e.Event.Value == "hidden" {
				return a.closeModels()
			}
		case "input":
			if e.Event.Ref == "prompt" {
				if a.selected != nil {
					a.selected.draft = e.Event.Value
				} else {
					a.drafts[a.context.ProjectPath] = e.Event.Value
				}
			}
		case "submit":
			return a.send()
		case "click":
			switch e.Event.Ref {
			case "send":
				return a.send()
			case "new":
				a.composerMenu = false
				a.selected = nil
				a.status = ""
				a.showHistory = false
				a.panelDirty = true
			case "stop":
				return a.interrupt(a.selected)
			case "history":
				a.composerMenu = false
				return a.listHistory("")
			case "composer-menu":
				a.composerMenu = !a.composerMenu
				a.panelDirty = true
				return a.closeModels()
			case "model-settings":
				if a.modelSettings {
					return a.closeModels()
				}
				return a.openModels()
			case "more-history":
				return a.listHistory(a.history.NextCursor)
			default:
				if id, ok := strings.CutPrefix(e.Event.Ref, "history/"); ok {
					return a.resume(id)
				}
			}
		}
		return nil
	}
	c := a.conversations[e.ID]
	if c == nil {
		return nil
	}
	switch e.Event.Kind {
	case "close":
		c.open = false
		if !c.busy {
			c.items = nil
			c.loaded = false
		}
	case "visibilitychange":
		if e.Event.Value == "visible" {
			a.selected = c
			a.context = c.context
			a.panelDirty = true
		}
	case "input":
		parts := strings.SplitN(e.Event.Ref, "/", 3)
		if len(parts) == 3 && parts[0] == "answer" {
			for _, r := range c.requests {
				if string(r.id) == parts[1] {
					r.answers[parts[2]] = e.Event.Value
				}
			}
		}
	case "click":
		if e.Event.Ref == "older" {
			return a.loadHistory(c)
		}
		if e.Event.Ref == "compose" {
			a.selected = c
			a.context = c.context
			a.panelOpen = true
			a.panelDirty = true
		}
		if e.Event.Ref == "stop" {
			return a.interrupt(c)
		}
		parts := strings.SplitN(e.Event.Ref, "/", 2)
		if len(parts) == 2 && (parts[0] == "accept" || parts[0] == "decline" || parts[0] == "answer") {
			return a.answer(c, parts[1], parts[0])
		}
	}
	return nil
}

func (a *agent) startThread(prompt string) (*conversation, error) {
	params := map[string]any{"cwd": a.context.ProjectPath, "approvalPolicy": "on-request", "sandbox": "workspace-write"}
	if a.model != "" {
		params["model"] = a.model
	}
	result, err := call[struct {
		Thread thread `json:"thread"`
	}](a.client, "thread/start", params)
	if err != nil {
		return nil, err
	}
	if result.Thread.ID == "" {
		return nil, fmt.Errorf("Codex 未返回会话 ID")
	}
	c := &conversation{thread: result.Thread, context: a.context, loaded: true, open: true, dirty: true}
	if c.Cwd == "" {
		c.Cwd = a.context.ProjectPath
	}
	c.Name = shortTitle(prompt)
	a.conversations[c.ID] = c
	a.selected = c
	return c, nil
}

func shortTitle(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > 40 {
		return string(runes[:40]) + "…"
	}
	if text == "" {
		return "Codex 新会话"
	}
	return text
}

func (a *agent) send() error {
	c := a.selected
	prompt := a.drafts[a.context.ProjectPath]
	if c != nil {
		prompt = c.draft
		a.context = c.context
	}
	if strings.TrimSpace(prompt) == "" {
		return nil
	}
	if c != nil && c.busy {
		return fmt.Errorf("当前会话正在工作，请等待完成或点击停止")
	}
	if err := a.connect(); err != nil {
		return err
	}
	fresh := c == nil
	if fresh {
		var err error
		c, err = a.startThread(prompt)
		if err != nil {
			return err
		}
		c.draft = prompt
	} else if !c.loaded {
		if err := a.resume(c.ID); err != nil {
			return err
		}
	}
	if c.busy {
		return fmt.Errorf("当前会话仍有任务在运行，请等待完成或点击停止")
	}
	// Publish a fresh tab before starting work, then stream updates without
	// revealing it again when another conversation is selected.
	c.open, c.dirty = true, true
	a.flush()
	params := map[string]any{"threadId": c.ID, "input": []input{{Type: "text", Text: prompt, TextElements: []struct{}{}}}}
	if a.model != "" {
		params["model"] = a.model
	}
	c.busy = true
	c.status = ""
	result, err := call[struct {
		Turn turn `json:"turn"`
	}](a.client, "turn/start", params)
	if err != nil {
		c.busy = false
		c.dirty = true
		return err
	}
	c.busy = c.busy && result.Turn.Status == "inProgress"
	if c.busy {
		c.turnID = result.Turn.ID
	}
	c.draft = ""
	if fresh {
		delete(a.drafts, a.context.ProjectPath)
	}
	a.status = ""
	a.panelDirty, c.dirty = true, true
	if fresh {
		_, err = call[json.RawMessage](a.client, "thread/name/set", map[string]string{"threadId": c.ID, "name": c.Name})
		if err != nil {
			fmt.Fprintln(stderrWriter, err)
		}
	}
	return nil
}

func (a *agent) interrupt(c *conversation) error {
	if c == nil || !c.busy || c.turnID == "" || a.client == nil {
		return nil
	}
	_, err := call[json.RawMessage](a.client, "turn/interrupt", map[string]string{"threadId": c.ID, "turnId": c.turnID})
	return err
}

func (a *agent) listHistory(cursor string) error {
	if err := a.connect(); err != nil {
		return err
	}
	page, err := call[threadPage](a.client, "thread/list", map[string]any{"cwd": a.context.ProjectPath, "limit": 20, "sortKey": "updated_at", "cursor": cursor})
	if err != nil {
		return err
	}
	if cursor != "" {
		page.Data = append(a.history.Data, page.Data...)
	}
	a.history = page
	a.showHistory, a.panelDirty = true, true
	return nil
}

func (a *agent) resume(id string) error {
	if err := a.connect(); err != nil {
		return err
	}
	c := a.conversations[id]
	if c == nil || !c.loaded {
		result, err := call[struct {
			Thread thread `json:"thread"`
		}](a.client, "thread/resume", map[string]any{"threadId": id, "excludeTurns": true, "approvalPolicy": "on-request", "sandbox": "workspace-write"})
		if err != nil {
			return err
		}
		if c == nil {
			c = &conversation{thread: result.Thread, context: a.context}
			a.conversations[id] = c
		}
		c.thread = result.Thread
		c.loaded = true
		c.items = nil
		c.cursor = ""
		if err = a.loadHistory(c); err != nil {
			c.loaded = false
			return err
		}
	}
	a.selected = c
	a.context = c.context
	a.showHistory = false
	wasOpen := c.open
	c.open, c.dirty = true, true
	a.panelDirty = true
	a.flush()
	if wasOpen {
		return a.publisher.Show(peb.UI{ID: c.ID, Title: c.title(), Kind: peb.EditorTab, Context: c.context})
	}
	return nil
}

func (c *conversation) title() string {
	if c.Name != "" {
		return c.Name
	}
	return shortTitle(c.Preview)
}

func (a *agent) loadHistory(c *conversation) error {
	if c.busy && (len(c.items) > 0 || c.cursor != "") {
		return fmt.Errorf("请在当前任务结束后加载更早记录")
	}
	revision := c.turnRevision
	page, err := call[struct {
		Data       []turn `json:"data"`
		NextCursor string `json:"nextCursor"`
	}](a.client, "thread/turns/list", map[string]any{"threadId": c.ID, "limit": 10, "sortDirection": "desc", "itemsView": "full", "cursor": c.cursor})
	if err != nil {
		return err
	}
	var older []*chatItem
	slices.Reverse(page.Data)
	for _, t := range page.Data {
		if t.Status == "inProgress" && c.turnRevision == revision {
			c.busy = true
			c.turnID = t.ID
		}
		for _, item := range t.Items {
			if !slices.ContainsFunc(c.items, func(i *chatItem) bool { return i.data.ID == item.ID }) {
				older = append(older, newChatItem(item, t.Status != "inProgress"))
			}
		}
	}
	c.items = append(older, c.items...)
	c.cursor = page.NextCursor
	c.dirty = true
	return nil
}

func newChatItem(item threadItem, complete bool) *chatItem {
	i := &chatItem{}
	i.replace(item, complete)
	return i
}

func (i *chatItem) replace(item threadItem, complete bool) {
	i.data, i.complete = item, complete
	i.html = ""
	i.text.Reset()
	i.output.Reset()
	appendText(&i.text, item.Text)
	for _, part := range item.Content {
		if part.Type == "text" {
			appendText(&i.text, part.Text)
		}
	}
	if item.Type == "reasoning" {
		for _, s := range item.Summary {
			appendText(&i.text, s+"\n")
		}
	}
	appendText(&i.output, item.AggregatedOutput)
	// Builders own bounded display text; don't retain duplicate large payloads.
	i.data.Text = ""
	i.data.Content = nil
	i.data.Summary = nil
	i.data.AggregatedOutput = ""
}

func appendText(b *strings.Builder, text string) {
	const limit = 128 << 10
	if b.Len() >= limit {
		return
	}
	text = text[:min(len(text), limit-b.Len())]
	// Keep a valid UTF-8 boundary when truncating streamed text.
	text = strings.ToValidUTF8(text, "")
	b.WriteString(text)
}

func (c *conversation) item(id string) *chatItem {
	for _, item := range c.items {
		if item.data.ID == id {
			return item
		}
	}
	i := newChatItem(threadItem{ID: id, Type: "agentMessage"}, false)
	c.items = append(c.items, i)
	return i
}

func (a *agent) onMessage(message envelope) {
	if message.Method == "" {
		return
	}
	var event struct {
		ThreadID   string          `json:"threadId"`
		ItemID     string          `json:"itemId"`
		Delta      string          `json:"delta"`
		ThreadName string          `json:"threadName"`
		RequestID  json.RawMessage `json:"requestId"`
		Item       threadItem      `json:"item"`
		Turn       turn            `json:"turn"`
		Error      *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(message.Params, &event); err != nil {
		fmt.Fprintln(stderrWriter, err)
		return
	}
	c := a.conversations[event.ThreadID]
	if c == nil {
		if len(message.ID) > 0 {
			_ = a.client.reply(message.ID, nil, &rpcError{Code: -32601, Message: "No conversation owns this request"})
		}
		return
	}
	if len(message.ID) > 0 {
		switch message.Method {
		case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "item/tool/requestUserInput":
			c.requests = append(c.requests, &approval{id: message.ID, method: message.Method, params: message.Params, answers: make(map[string]string)})
		default:
			_ = a.client.reply(message.ID, nil, &rpcError{Code: -32601, Message: "Pencode does not implement " + message.Method})
			a.status = "暂不支持 Codex 请求：" + message.Method
			a.panelDirty = true
		}
		c.dirty = true
		return
	}
	switch message.Method {
	case "item/started", "item/completed":
		c.item(event.Item.ID).replace(event.Item, message.Method == "item/completed")
	case "item/agentMessage/delta", "item/plan/delta", "item/reasoning/summaryTextDelta":
		i := c.item(event.ItemID)
		appendText(&i.text, event.Delta)
		i.html = ""
	case "item/commandExecution/outputDelta":
		i := c.item(event.ItemID)
		appendText(&i.output, event.Delta)
		i.html = ""
	case "thread/name/updated":
		c.Name = event.ThreadName
	case "turn/started":
		c.turnRevision++
		c.busy = true
		c.status = ""
		c.turnID = event.Turn.ID
		a.panelDirty = true
	case "turn/completed":
		c.turnRevision++
		c.busy = false
		c.turnID = ""
		c.requests = nil
		a.panelDirty = true
		if event.Turn.Error != nil {
			c.status = event.Turn.Error.Message
		}
		if event.Turn.Status == "interrupted" {
			c.status = "任务已停止"
		}
		if !c.open {
			c.items = nil
			c.loaded = false
		}
	case "serverRequest/resolved":
		c.requests = slices.DeleteFunc(c.requests, func(r *approval) bool { return string(r.id) == string(event.RequestID) })
	case "error":
		if event.Error != nil {
			c.status = event.Error.Message
			a.panelDirty = true
		}
	default:
		return
	}
	c.dirty = true
}

func (a *agent) answer(c *conversation, id, decision string) error {
	if a.client == nil {
		return fmt.Errorf("Codex 已断开连接")
	}
	for index, r := range c.requests {
		if string(r.id) != id {
			continue
		}
		var result any
		switch r.method {
		case "item/tool/requestUserInput":
			answers := make(map[string]any, len(r.answers))
			var params struct {
				Questions []struct {
					ID string `json:"id"`
				} `json:"questions"`
			}
			if err := json.Unmarshal(r.params, &params); err != nil {
				return err
			}
			for _, q := range params.Questions {
				text := strings.TrimSpace(r.answers[q.ID])
				if text == "" {
					return fmt.Errorf("请回答所有问题")
				}
				answers[q.ID] = map[string]any{"answers": []string{text}}
			}
			result = map[string]any{"answers": answers}
		case "item/permissions/requestApproval":
			var params struct {
				Permissions json.RawMessage `json:"permissions"`
			}
			if err := json.Unmarshal(r.params, &params); err != nil {
				return err
			}
			if decision != "accept" {
				params.Permissions = json.RawMessage(`{}`)
			}
			result = map[string]any{"permissions": params.Permissions, "scope": "turn"}
		default:
			value := "decline"
			if decision == "accept" {
				value = "accept"
			}
			result = map[string]string{"decision": value}
		}
		if err := a.client.reply(r.id, result, nil); err != nil {
			return err
		}
		c.requests = slices.Delete(c.requests, index, index+1)
		c.dirty = true
		return nil
	}
	return nil
}
