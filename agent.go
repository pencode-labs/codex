package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
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
	Kind struct {
		Type string `json:"type"`
	} `json:"kind"`
}
type commandAction struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Query   string `json:"query"`
}
type threadItem struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Text             string          `json:"text"`
	Content          []input         `json:"content"`
	Summary          []string        `json:"summary"`
	Command          string          `json:"command"`
	CommandActions   []commandAction `json:"commandActions"`
	Status           string          `json:"status"`
	AggregatedOutput string          `json:"aggregatedOutput"`
	Changes          []change        `json:"changes"`
	Server           string          `json:"server"`
	Tool             string          `json:"tool"`
	Query            string          `json:"query"`
}
type turn struct {
	ID          string       `json:"id"`
	Status      string       `json:"status"`
	Items       []threadItem `json:"items"`
	Error       *rpcError    `json:"error"`
	StartedAt   int64        `json:"startedAt"`
	CompletedAt int64        `json:"completedAt"`
	DurationMs  *int64       `json:"durationMs"`
}
type thread struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Preview string `json:"preview"`
}
type chatItem struct {
	data          threadItem
	progress      *turnProgress
	files         *turnFiles
	text, output  strings.Builder
	complete      bool
	expanded      bool
	detailRef     string
	groupRef      string
	groupExpanded bool
	html          string
}
type approval struct {
	id      json.RawMessage
	method  string
	params  json.RawMessage
	answers map[string]string
}
type conversation struct {
	thread
	titleJob              *titleJob
	progress              *turnProgress
	files                 *turnFiles
	context               peb.ActionContext
	items                 []*chatItem
	requests              []*approval
	draft, turnID, cursor string
	status                string
	turnRevision          uint64
	busy, dirty, loaded   bool
	exitRequested         bool
}
type threadPage struct {
	Data       []thread `json:"data"`
	NextCursor string   `json:"nextCursor"`
}

func (p *threadPage) contains(id string) bool {
	return slices.ContainsFunc(p.Data, func(t thread) bool { return t.ID == id })
}

func (p *threadPage) include(t thread) {
	if !p.contains(t.ID) {
		p.Data = slices.Insert(p.Data, 0, t)
	}
}

func (p *threadPage) compact() {
	data := p.Data
	p.Data = data[:0]
	for _, t := range data {
		if t.ID != "" && !p.contains(t.ID) {
			p.Data = append(p.Data, t)
		}
	}
	clear(data[len(p.Data):])
}

type agent struct {
	ctx                                        context.Context
	publisher                                  *peb.ActionPublisher
	commands                                   chan command
	pending                                    [64]command
	pendingHead, pendingCount                  int
	done                                       chan struct{}
	client                                     *rpcClient
	executablePath                             string
	context                                    peb.ActionContext
	conversations                              map[string]*conversation
	selected                                   *conversation
	drafts                                     map[string]string
	attachments                                map[attachmentScope][]string
	filePicker                                 *filePicker
	model, status                              string
	effort, pendingModel                       string
	history                                    threadPage
	showHistory, workspaceOpen, workspaceDirty bool
	historyDirty, historyLoading               bool
	composerPrefix                             string
	composerBindings                           []peb.EditorBinding
	revealWorkspace                            bool
	modelSettings                              bool
	models                                     []modelOption
	modelsLoaded                               bool
	modelError                                 string
	permissionIndex                            int
	permissionSettings, permissionHelp         bool
	renameID, renameValue                      string
	exitsPending                               bool
}

func (a *agent) run() {
	defer close(a.done)
	defer a.closeClient()
	a.conversations = make(map[string]*conversation)
	a.drafts = make(map[string]string)
	flush := time.NewTimer(time.Hour)
	flush.Stop()
	defer flush.Stop()
	progressTimer := time.NewTimer(time.Hour)
	progressTimer.Stop()
	defer progressTimer.Stop()
	var progressTick <-chan time.Time
	var tick <-chan time.Time
	for {
		if a.exitsPending {
			a.exitConversations()
		}
		var messages <-chan envelope
		if a.client != nil {
			messages = a.client.messages
		}
		if a.pendingCount > 0 {
			cmd := a.pending[a.pendingHead]
			a.pending[a.pendingHead] = command{}
			a.pendingHead = (a.pendingHead + 1) % len(a.pending)
			a.pendingCount--
			a.processCommand(cmd)
			a.workspaceDirty = true
		} else {
			select {
			case <-a.ctx.Done():
				return
			case cmd := <-a.commands:
				a.processCommand(cmd)
			case message, ok := <-messages:
				if !ok {
					a.disconnected()
					break
				}
				a.onMessage(message)
			case <-progressTick:
				a.updateProgress(time.Now())
				progressTick = nil
			case <-tick:
				a.flush()
				tick = nil
			}
		}
		if a.progressVisible() {
			if progressTick == nil {
				progressTimer.Reset(time.Second)
				progressTick = progressTimer.C
			}
		} else if progressTick != nil {
			progressTimer.Stop()
			progressTick = nil
		}
		if tick == nil && a.dirty() {
			flush.Reset(80 * time.Millisecond)
			tick = flush.C
		}
	}
}

func (a *agent) processCommand(cmd command) {
	a.reportError(a.handleCommand(cmd))
}

func (a *agent) reportError(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(stderrWriter, err)
	if err := a.publisher.Show(peb.UI{
		ID: "error", Title: "Codex", Kind: peb.Notification, Context: a.context,
		View: &peb.EditorView{HTML: "<p>" + html.EscapeString(err.Error()) + "</p>", CSS: "p { white-space: pre-wrap; }"},
	}); err != nil {
		fmt.Fprintln(stderrWriter, err)
	}
}

// RPC waits still service tab lifecycle events. Other commands stay ordered
// until the current request completes, without starting nested RPC calls.
func (a *agent) handlePendingCommand(cmd command) {
	if cmd.action && cmd.context.ProjectPath == a.context.ProjectPath {
		a.reportError(a.openWorkspace(cmd.context))
		return
	}
	if !cmd.action {
		switch cmd.event.Event.Kind {
		case "open", "close", "visibilitychange":
			a.processCommand(cmd)
			return
		}
	}
	if a.pendingCount == len(a.pending) {
		a.reportError(fmt.Errorf("Codex 事件队列已满"))
		return
	}
	a.pending[(a.pendingHead+a.pendingCount)%len(a.pending)] = cmd
	a.pendingCount++
}

func (a *agent) dirty() bool {
	return a.workspaceOpen && (a.workspaceDirty || a.historyDirty || a.selected != nil && a.selected.dirty)
}

// Release the previous conversation after any pending RPC returns. Drafts and
// history remain available when the conversation is resumed later.
func (a *agent) selectConversation(c *conversation) {
	if a.selected != c {
		if previous := a.selected; previous != nil {
			previous.exitRequested, a.exitsPending = true, true
			if !previous.busy {
				previous.items = nil
				previous.files = nil
				previous.loaded = false
			}
		}
		a.selected = c
	}
	a.workspaceDirty = true
}

func (a *agent) closeWorkspace() error {
	a.workspaceOpen, a.revealWorkspace = false, false
	a.composerPrefix, a.composerBindings = "", nil
	if a.client != nil {
		a.exitsPending = true
	}
	a.selectConversation(nil)
	for _, c := range a.conversations {
		if !c.busy {
			c.items = nil
			c.files = nil
			c.loaded = false
		}
	}
	return a.closeComposerMenus()
}

func (a *agent) exitConversations() {
	if !a.exitsPending {
		return
	}
	a.exitsPending = false
	for _, c := range a.conversations {
		if !c.exitRequested {
			continue
		}
		// Tab closes can arrive inside an RPC wait. Interrupt from the actor
		// loop after that request returns, including a pending turn/start.
		a.reportError(a.interrupt(c))
	}
	// Unsubscribe alone retains the thread for an inactivity grace period.
	// Reap our server before opening another conversation to release it now.
	a.closeClient()
}

func (a *agent) closeClient() {
	a.client.close()
	a.client = nil
	for _, c := range a.conversations {
		if c.titleJob != nil {
			c.titleJob.cancel()
			c.titleJob = nil
		}
		c.finishProgress(turn{}, time.Now())
		c.items, c.requests = nil, nil
		c.files = nil
		c.loaded, c.busy, c.exitRequested = false, false, false
		c.turnID = ""
	}
}

func (a *agent) disconnected() {
	a.client.close()
	err := a.client.err
	a.client = nil
	a.models, a.modelsLoaded = nil, false
	a.status = fmt.Sprintf("Codex 连接已断开：%v。重新发送或打开历史可重连。", err)
	for _, c := range a.conversations {
		c.finishProgress(turn{}, time.Now())
		c.loaded, c.busy = false, false
		c.turnID = ""
		c.requests = nil
		c.status = a.status
		c.dirty = true
	}
	a.workspaceDirty = true
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
	client, err := startClient(a.ctx, root, a.executablePath, a.onMessage)
	if err != nil {
		return err
	}
	a.client = client
	client.commands, client.dispatch = a.commands, a.handlePendingCommand
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

func (a *agent) openWorkspace(ctx peb.ActionContext) error {
	err := a.setContext(ctx)
	a.revealWorkspace = a.workspaceOpen
	a.workspaceOpen, a.workspaceDirty = true, true
	if err != nil {
		return err
	}
	// Open or reveal the tab before waiting for app-server or stored history.
	a.flush()
	return nil
}

func (a *agent) handleCommand(cmd command) error {
	if cmd.title != nil {
		return a.finishTitle(cmd.title)
	}
	if cmd.settings != nil {
		path, err := peb.DecodeSetting[string](cmd.settings, "executablePath")
		if err == nil {
			a.executablePath = path
		}
		return err
	}
	if cmd.action {
		if err := a.openWorkspace(cmd.context); err != nil {
			return err
		}
		if a.selected != nil && !a.selected.loaded {
			if err := a.resume(a.selected.ID); err != nil {
				return err
			}
		}
		return a.listHistory("")
	}
	e := cmd.event
	if e.ID == "rename" {
		return a.handleRenameEvent(e.Event)
	}
	if e.ID == "models" {
		return a.handleModelEvent(e.Event)
	}
	if e.ID == "permissions" {
		return a.handlePermissionEvent(e.Event)
	}
	if e.ID == "files" {
		return a.handleFileEvent(e.Event)
	}
	if e.ID != "composer" {
		return nil
	}
	if e.Event.Kind == "projectchange" {
		if e.Context.ProjectPath == a.context.ProjectPath {
			return nil
		}
		if err := a.setContext(e.Context); err != nil {
			return err
		}
		a.flush()
		if filepath.IsAbs(a.context.ProjectPath) {
			return a.listHistory("")
		}
		return nil
	}
	c := a.selected
	switch e.Event.Kind {
	case "close":
		return a.closeWorkspace()
	case "visibilitychange":
		if e.Event.Value == "hidden" {
			return a.closeComposerMenus()
		}
	case "input":
		if e.Event.Ref == "prompt" {
			if c != nil {
				c.draft = e.Event.Value
			} else {
				a.drafts[a.context.ProjectPath] = e.Event.Value
			}
		} else if c != nil {
			parts := strings.SplitN(e.Event.Ref, "/", 3)
			if len(parts) == 3 && parts[0] == "answer" {
				for _, r := range c.requests {
					if string(r.id) == parts[1] {
						r.answers[parts[2]] = e.Event.Value
					}
				}
			}
		}
	case "submit":
		return a.send()
	case "click":
		switch e.Event.Ref {
		case "send":
			return a.send()
		case "new":
			if err := a.closeComposerMenus(); err != nil {
				return err
			}
			a.selectConversation(nil)
			a.status = ""
		case "stop":
			return a.interrupt(c)
		case "history", "refresh-history":
			return a.listHistory("")
		case "add-file":
			return a.openFiles()
		case "permission-settings":
			if a.permissionSettings {
				return a.closePermissions()
			}
			return a.openPermissions()
		case "model-settings":
			if a.modelSettings {
				return a.closeModels()
			}
			return a.openModels()
		case "more-history":
			return a.listHistory(a.history.NextCursor)
		case "older":
			if c != nil {
				return a.loadHistory(c)
			}
		default:
			if c != nil && c.handleChangesClick(e.Event.Ref) {
				return nil
			}
			if id, ok := strings.CutPrefix(e.Event.Ref, "rename/"); ok {
				return a.openRename(id)
			}
			if index, ok := strings.CutPrefix(e.Event.Ref, "remove-file/"); ok {
				a.removeAttachment(index)
				return nil
			}
			if c != nil && strings.HasPrefix(e.Event.Ref, "item/") {
				for _, i := range c.items {
					if i.detailRef == e.Event.Ref {
						i.expanded = !i.expanded
						i.html = ""
						c.dirty = true
						break
					}
				}
				return nil
			}
			if c != nil && strings.HasPrefix(e.Event.Ref, "activity/") {
				for _, i := range c.items {
					if i.groupRef == e.Event.Ref {
						i.groupExpanded = !i.groupExpanded
						c.dirty = true
						break
					}
				}
				return nil
			}
			if id, ok := strings.CutPrefix(e.Event.Ref, "history/"); ok {
				return a.resume(id)
			}
			parts := strings.SplitN(e.Event.Ref, "/", 2)
			if c != nil && len(parts) == 2 && (parts[0] == "accept" || parts[0] == "decline" || parts[0] == "answer") {
				return a.answer(c, parts[1], parts[0])
			}
		}
	}
	return nil
}

func (a *agent) startThread() (*conversation, error) {
	a.selectConversation(nil)
	a.exitConversations()
	if err := a.connect(); err != nil {
		return nil, err
	}
	p := a.permission()
	params := struct {
		Cwd     string `json:"cwd"`
		Model   string `json:"model,omitempty"`
		Sandbox string `json:"sandbox"`
		approvalSettings
	}{a.context.ProjectPath, a.model, p.sandbox, p.approvalSettings}
	result, err := call[struct {
		Thread thread `json:"thread"`
	}](a.client, "thread/start", params)
	if err != nil {
		return nil, err
	}
	if result.Thread.ID == "" {
		return nil, fmt.Errorf("Codex 未返回会话 ID")
	}
	c := &conversation{thread: result.Thread, context: a.context, loaded: true, dirty: true}
	a.conversations[c.ID] = c
	a.history.include(c.thread)
	a.showHistory = true
	if a.exitsPending {
		c.exitRequested = true
	} else {
		a.selectConversation(c)
	}
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
	scope := a.attachmentScope()
	files := a.attachments[scope]
	if strings.TrimSpace(prompt) == "" && len(files) == 0 {
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
		c, err = a.startThread()
		if err != nil {
			return err
		}
		c.draft = prompt
		if len(files) > 0 {
			a.attachments[a.attachmentScope()] = files
			delete(a.attachments, scope)
		}
	} else if !c.loaded {
		if err := a.resume(c.ID); err != nil {
			return err
		}
	}
	if c.busy {
		return fmt.Errorf("当前会话仍有任务在运行，请等待完成或点击停止")
	}
	if !a.workspaceOpen || c.exitRequested {
		a.selected = nil
		return nil
	}
	// Show the stop action before waiting for turn/start to acknowledge the task.
	c.busy, c.dirty = true, true
	c.progress = &turnProgress{started: time.Now()}
	c.files = newTurnFiles("", c.context.ProjectPath)
	c.status = ""
	a.flush()
	p := a.permission()
	params := struct {
		ThreadID      string        `json:"threadId"`
		Input         [1]input      `json:"input"`
		Model         string        `json:"model,omitempty"`
		Effort        string        `json:"effort,omitempty"`
		SandboxPolicy sandboxPolicy `json:"sandboxPolicy"`
		approvalSettings
	}{c.ID, [1]input{{Type: "text", Text: attachmentPrompt(prompt, files), TextElements: []struct{}{}}}, a.model, a.turnEffort(), p.policy, p.approvalSettings}
	result, err := call[struct {
		Turn turn `json:"turn"`
	}](a.client, "turn/start", params)
	if err != nil {
		c.finishProgress(turn{}, time.Now())
		c.busy = false
		c.dirty = true
		return err
	}
	c.busy = c.busy && result.Turn.Status == "inProgress"
	if c.busy {
		c.turnID = result.Turn.ID
		c.startProgress(result.Turn, time.Now())
	} else {
		c.finishProgress(result.Turn, time.Now())
	}
	c.draft = ""
	delete(a.attachments, a.attachmentScope())
	if fresh {
		delete(a.drafts, a.context.ProjectPath)
	}
	a.status = ""
	a.workspaceDirty, c.dirty = true, true
	if c.Name == "" && c.titleJob == nil && !c.exitRequested {
		a.generateTitle(c, attachmentPrompt(prompt, files))
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
	a.historyLoading, a.historyDirty = true, true
	if a.publisher != nil {
		a.flush()
	}
	defer func() {
		a.historyLoading, a.historyDirty = false, true
	}()
	if err := a.connect(); err != nil {
		return err
	}
	page, err := call[threadPage](a.client, "thread/list", struct {
		Cwd            string    `json:"cwd"`
		Limit          int       `json:"limit"`
		SortKey        string    `json:"sortKey"`
		Cursor         string    `json:"cursor,omitempty"`
		SourceKinds    [3]string `json:"sourceKinds"`
		UseStateDBOnly bool      `json:"useStateDbOnly"`
	}{a.context.ProjectPath, 20, "updated_at", cursor, [3]string{"cli", "vscode", "appServer"}, true})
	if err != nil {
		return err
	}
	page.compact()
	if cursor != "" {
		page.Data = slices.DeleteFunc(page.Data, func(t thread) bool { return a.history.contains(t.ID) })
		page.Data = append(a.history.Data, page.Data...)
	}
	// Newly started threads may not have reached the server's history index yet.
	for _, c := range a.conversations {
		if c.context.ProjectPath == a.context.ProjectPath {
			page.include(c.thread)
		}
	}
	a.history = page
	a.showHistory, a.historyDirty = true, true
	return nil
}

func (a *agent) resume(id string) error {
	if a.selected != nil && a.selected.ID != id {
		a.selectConversation(nil)
	}
	c := a.conversations[id]
	if c != nil {
		if err := a.setContext(c.context); err != nil {
			return err
		}
	}
	a.exitConversations()
	if err := a.connect(); err != nil {
		return err
	}
	if c == nil || !c.loaded {
		p := a.permission()
		result, err := call[struct {
			Thread thread `json:"thread"`
		}](a.client, "thread/resume", struct {
			ThreadID     string `json:"threadId"`
			ExcludeTurns bool   `json:"excludeTurns"`
			Sandbox      string `json:"sandbox"`
			approvalSettings
		}{id, true, p.sandbox, p.approvalSettings})
		if err != nil {
			return err
		}
		if c == nil {
			c = &conversation{thread: result.Thread, context: a.context}
			a.conversations[id] = c
		}
		c.thread = result.Thread
		c.loaded = true
		if a.exitsPending {
			c.exitRequested = true
			return nil
		}
		c.items = nil
		c.progress = nil
		c.files = nil
		c.cursor = ""
		if err = a.loadHistory(c); err != nil {
			a.closeClient()
			return err
		}
	}
	if a.exitsPending {
		c.exitRequested = true
		return nil
	}
	a.history.include(c.thread)
	a.showHistory = true
	a.selectConversation(c)
	c.dirty = true
	return nil
}

func (c *conversation) title() string {
	if c.Name != "" {
		return c.Name
	}
	if c.titleJob != nil {
		return "正在生成标题…"
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
	}](a.client, "thread/turns/list", struct {
		ThreadID      string `json:"threadId"`
		Limit         int    `json:"limit"`
		SortDirection string `json:"sortDirection"`
		ItemsView     string `json:"itemsView"`
		Cursor        string `json:"cursor,omitempty"`
	}{c.ID, 10, "desc", "full", c.cursor})
	if err != nil {
		return err
	}
	var older []*chatItem
	slices.Reverse(page.Data)
	for _, t := range page.Data {
		files := c.turnFiles(t.ID)
		if files == nil {
			files = newTurnFiles(t.ID, c.context.ProjectPath)
		}
		collect := !files.complete && t.Status != "inProgress"
		var progress *turnProgress
		if c.progress != nil && c.progress.id == t.ID {
			progress = c.progress
		} else if t.DurationMs != nil || t.StartedAt > 0 && t.CompletedAt > 0 {
			progress = &turnProgress{}
			progress.update(t, time.Now())
		}
		if t.Status == "inProgress" && c.turnRevision == revision {
			c.busy = true
			c.turnID = t.ID
			c.startProgress(t, time.Now())
			progress = c.progress
			c.files = files
		}
		for _, item := range t.Items {
			if collect {
				files.add(item)
			}
			if !slices.ContainsFunc(c.items, func(i *chatItem) bool { return i.data.ID == item.ID }) {
				i := newChatItem(item, t.Status != "inProgress")
				i.progress = progress
				i.files = files
				if progress != nil && i.modelText() {
					progress.received = true
				}
				older = append(older, i)
			}
		}
		files.complete = files.complete || collect
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
	i.progress = c.progress
	i.files = c.files
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
	if message.Method == "thread/name/updated" {
		a.updateThreadName(event.ThreadID, event.ThreadName)
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
			a.workspaceDirty = true
		}
		c.dirty = true
		return
	}
	switch message.Method {
	case "item/started", "item/completed":
		i := c.item(event.Item.ID)
		i.replace(event.Item, message.Method == "item/completed")
		if i.progress != nil && i.modelText() {
			i.progress.received = true
		}
	case "item/agentMessage/delta", "item/plan/delta", "item/reasoning/summaryTextDelta":
		i := c.item(event.ItemID)
		appendText(&i.text, event.Delta)
		i.html = ""
		if c.progress != nil && event.Delta != "" {
			c.progress.received = true
		}
	case "item/reasoning/textDelta":
		if c.progress != nil && event.Delta != "" {
			c.progress.received = true
		}
	case "item/commandExecution/outputDelta":
		i := c.item(event.ItemID)
		appendText(&i.output, event.Delta)
		i.html = ""
	case "turn/started":
		c.turnRevision++
		c.busy = true
		c.status = ""
		c.turnID = event.Turn.ID
		c.startProgress(event.Turn, time.Now())
		if c.files == nil || c.files.id != "" && c.files.id != event.Turn.ID {
			c.files = newTurnFiles(event.Turn.ID, c.context.ProjectPath)
		} else {
			c.files.id = event.Turn.ID
			c.files.ref = "changes/" + event.Turn.ID
		}
	case "turn/completed":
		c.finishProgress(event.Turn, time.Now())
		if c.files != nil && !c.files.complete {
			for _, i := range c.items {
				if i.files == c.files {
					c.files.add(i.data)
				}
			}
			c.files.complete = true
		}
		c.turnRevision++
		c.busy = false
		c.turnID = ""
		c.requests = nil
		if event.Turn.Error != nil {
			c.status = event.Turn.Error.Message
		}
		if event.Turn.Status == "interrupted" {
			c.status = "任务已停止"
		}
		if !a.workspaceOpen || a.selected != c {
			c.items = nil
			c.files = nil
			c.loaded = false
		}
	case "serverRequest/resolved":
		c.requests = slices.DeleteFunc(c.requests, func(r *approval) bool { return string(r.id) == string(event.RequestID) })
	case "error":
		if event.Error != nil {
			c.status = event.Error.Message
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
