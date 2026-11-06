package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pencode-labs/pencode/peb"
)

var stderrWriter = os.Stderr

type input struct {
	Type         string     `json:"type"`
	Text         string     `json:"text,omitempty"`
	Name         string     `json:"name,omitempty"`
	Path         string     `json:"path,omitempty"`
	URL          string     `json:"url,omitempty"`
	TextElements []struct{} `json:"text_elements,omitempty"`
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
	ExitCode         *int            `json:"exitCode"`
	AggregatedOutput string          `json:"aggregatedOutput"`
	Query            string          `json:"query"`
	Path             string          `json:"path"`
	Type             string          `json:"type"`
	Text             string          `json:"text"`
	Phase            string          `json:"phase"`
	Tool             string          `json:"tool"`
	Server           string          `json:"server"`
	Command          string          `json:"command"`
	ID               string          `json:"id"`
	Status           string          `json:"status"`
	CommandActions   []commandAction `json:"commandActions"`
	Changes          []change        `json:"changes"`
	Summary          []string        `json:"summary"`
	Content          []input         `json:"content"`
	StartedAtMs      int64           `json:"startedAtMs"`
}
type turn struct {
	Error       *rpcError    `json:"error"`
	DurationMs  *int64       `json:"durationMs"`
	ID          string       `json:"id"`
	Status      string       `json:"status"`
	Items       []threadItem `json:"items"`
	StartedAt   int64        `json:"startedAt"`
	CompletedAt int64        `json:"completedAt"`
}
type thread struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Preview string       `json:"preview"`
	Status  threadStatus `json:"status"`
}
type threadStatus struct {
	Type string `json:"type"`
}
type chatItem struct {
	sentAt        time.Time
	progress      *turnProgress
	files         *turnFiles
	html          string
	detailRef     string
	groupRef      string
	text, output  strings.Builder
	images        []imageAttachment
	linkBindings  []peb.EditorBinding
	data          threadItem
	expanded      bool
	sweep         bool
	groupExpanded bool
	complete      bool
}
type approval struct {
	questions *userInputRequest
	method    string
	id        json.RawMessage
	params    json.RawMessage
}
type conversation struct {
	syncAfter time.Time
	editing   *chatItem
	titleJob  *titleJob
	progress  *turnProgress
	files     *turnFiles
	thread
	observedTurn, observedStatus string
	draft, turnID, cursor        string
	status                       string
	syncError                    string
	editDraft                    string
	requests                     []*approval
	items                        []*chatItem
	editInputs                   []input
	context                      peb.ActionContext
	turnRevision                 uint64
	editRevision                 uint64
	observedDigest               [32]byte
	unread                       bool
	observing                    bool
	busy, dirty, loaded          bool
	editReverted                 bool
}
type threadPage struct {
	NextCursor string   `json:"nextCursor"`
	Data       []thread `json:"data"`
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
	publisher                                  uiPublisher
	images                                     map[attachmentScope][]imageAttachment
	drafts                                     map[string]string
	done                                       chan struct{}
	client                                     *rpcClient
	filePicker                                 *filePicker
	attachments                                map[attachmentScope][]string
	commands                                   chan command
	conversations                              map[string]*conversation
	selected                                   *conversation
	effort, pendingModel                       string
	model, status                              string
	executablePath                             string
	serverURL                                  string
	composerPrefix                             string
	renameID, renameValue                      string
	modelError                                 string
	history                                    threadPage
	turnInputs                                 []input
	composerBindings                           []peb.EditorBinding
	models                                     []modelOption
	context                                    peb.ActionContext
	pending                                    [64]command
	completion                                 composerCompletion
	pendingHead, pendingCount                  int
	permissionIndex                            int
	modelSettings                              bool
	workspaceHidden                            bool
	modelsLoaded                               bool
	historyDirty, historyLoading               bool
	showHistory, workspaceOpen, workspaceDirty bool
	revealWorkspace                            bool
	permissionSettings, permissionHelp         bool
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
	syncTimer := time.NewTimer(time.Hour)
	syncTimer.Stop()
	defer syncTimer.Stop()
	var syncTick <-chan time.Time
	var progressTick <-chan time.Time
	var tick <-chan time.Time
	for {
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
			case <-syncTick:
				a.syncObserved()
				syncTick = nil
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
		if a.hasObservers() {
			if syncTick == nil {
				syncTimer.Reset(time.Second)
				syncTick = syncTimer.C
			}
		} else if syncTick != nil {
			syncTimer.Stop()
			syncTick = nil
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
		ID: "error", Title: "Codex", Kind: peb.Notification, Severity: peb.NotificationError, Context: a.context,
		View: &peb.EditorView{HTML: "<p>" + html.EscapeString(err.Error()) + "</p>", CSS: "p { white-space: pre-wrap; }"},
	}); err != nil {
		fmt.Fprintln(stderrWriter, err)
	}
}

// RPC waits still service tab lifecycle events and transcript toggles. Commands
// that may start another RPC stay ordered until the current request completes.
func (a *agent) handlePendingCommand(cmd command) {
	if cmd.lifecycle != nil && cmd.lifecycle.Kind != peb.ProjectClosed {
		a.processCommand(cmd)
		return
	}
	if cmd.action && cmd.context.ProjectPath == a.context.ProjectPath {
		a.reportError(a.openWorkspace(cmd.context))
		return
	}
	if !cmd.action {
		if cmd.event.ID == "composer" && cmd.event.Event.Kind == "click" && a.toggleTranscript(cmd.event.Event.Ref) {
			a.flush()
			return
		}
		switch cmd.event.Event.Kind {
		case "open", "close", "closerequest", "visibilitychange":
			a.processCommand(cmd)
			return
		}
	}
	if a.pendingCount == len(a.pending) {
		if cmd.reply != nil {
			cmd.reply <- peb.LifecycleReply{Message: "Codex 事件队列已满，未能完成项目清理。"}
		}
		a.reportError(fmt.Errorf("Codex 事件队列已满"))
		return
	}
	a.pending[(a.pendingHead+a.pendingCount)%len(a.pending)] = cmd
	a.pendingCount++
}

func (a *agent) dirty() bool {
	return a.workspaceOpen && (a.workspaceDirty || a.historyDirty || a.selected != nil && a.selected.dirty)
}

// Switching or closing the view keeps live subscriptions and approvals intact.
func (a *agent) selectConversation(c *conversation) {
	a.dismissCompletion()
	a.completion.dismissed = ""
	a.selected = c
	a.markViewed(c)
	a.workspaceDirty = true
}

func (a *agent) closeWorkspace() error {
	a.completion = composerCompletion{}
	a.workspaceOpen, a.revealWorkspace = false, false
	a.composerPrefix, a.composerBindings = "", nil
	return a.closeComposerMenus()
}

func (a *agent) closeClient() {
	a.completion.skillsLoaded = false
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
		c.loaded = false
		c.observing = false
		a.setBusy(c, false)
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
		c.loaded = false
		c.observing = false
		a.setBusy(c, false)
		c.turnID = ""
		c.requests = nil
		c.status = a.status
		c.dirty = true
	}
	a.workspaceDirty, a.historyDirty = true, true
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
	client, err := startClient(a.ctx, root, a.executablePath, a.serverURL, a.onMessage)
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
	a.workspaceOpen, a.workspaceDirty, a.workspaceHidden = true, true, false
	a.markViewed(a.selected)
	if err != nil {
		return err
	}
	// Open or reveal the tab before waiting for app-server or stored history.
	a.flush()
	return nil
}

func (a *agent) handleCommand(cmd command) error {
	if cmd.lifecycle != nil {
		cmd.reply <- a.handleLifecycle(*cmd.lifecycle)
		return nil
	}
	if cmd.title != nil {
		return a.finishTitle(cmd.title)
	}
	if cmd.settings != nil {
		path, err := peb.DecodeSetting[string](cmd.settings, "executablePath")
		if err == nil {
			a.executablePath = path
			a.serverURL, err = peb.DecodeSetting[string](cmd.settings, "serverURL")
		}
		return err
	}
	if cmd.action {
		if err := a.openWorkspace(cmd.context); err != nil {
			return err
		}
		if a.selected != nil && !a.selected.loaded {
			if err := a.openConversation(a.selected.ID); err != nil {
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
	if e.Event.Kind == "click" && (e.ID == "composer" || strings.HasPrefix(e.ID, "directory-")) {
		if ref, ok := strings.CutPrefix(e.Event.Ref, "directory/"); ok {
			name, _, _ := strings.Cut(ref, "/")
			return a.openDirectoryLink(name, e.Context)
		}
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
	if handled, err := a.handleQuestionEvent(e.Event); handled {
		return err
	}
	switch e.Event.Kind {
	case "closerequest":
		return a.requestCloseWorkspace()
	case "close":
		return a.closeWorkspace()
	case "visibilitychange":
		a.workspaceHidden = e.Event.Value == "hidden"
		if !a.workspaceHidden {
			a.markViewed(a.selected)
		}
		if a.workspaceHidden {
			return a.closeComposerMenus()
		}
	case "input":
		if e.Event.Ref == "prompt" {
			a.setDraft(e.Event.Value)
			a.updateCompletion(e.Event)
		} else if c != nil && e.Event.Ref == "message-edit-text" && c.editing != nil && !c.busy {
			c.editDraft = e.Event.Value
		}
	case "completion":
		if e.Event.Ref == "prompt" {
			return a.handleCompletion(e.Event)
		}
	case "paste":
		if e.Event.Ref == "prompt" {
			return a.pasteImage(e.Event)
		}
	case "submit":
		if e.Event.Ref == "message-edit-form" {
			return a.sendEditedMessage()
		}
		if a.completion.kind != 0 {
			return a.handleCompletion(peb.EditorEvent{Kind: "click", Data: "accept"})
		}
		return a.send()
	case "click":
		switch e.Event.Ref {
		case "message-edit-cancel":
			if c != nil && !c.busy {
				c.cancelEdit()
			}
			return nil
		case "new":
			a.selectConversation(nil)
			return a.closeComposerMenus()
		case "send":
			return a.send()
		case "stop":
			return a.interrupt(c)
		case "history", "refresh-history":
			return a.listHistory("")
		case "add-file":
			return a.openFiles()
		case "permission-settings":
			a.dismissCompletion()
			if a.permissionSettings {
				return a.closePermissions()
			}
			return a.openPermissions()
		case "model-settings":
			a.dismissCompletion()
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
			if a.toggleTranscript(e.Event.Ref) {
				return nil
			}
			if id, ok := strings.CutPrefix(e.Event.Ref, "edit-message/"); ok {
				return a.editMessage(id)
			}
			if index, ok := strings.CutPrefix(e.Event.Ref, "remove-image/"); ok {
				a.removeImage(index)
				return nil
			}
			if index, ok := strings.CutPrefix(e.Event.Ref, "preview-image/"); ok {
				return a.openImage(index)
			}
			if ref, ok := strings.CutPrefix(e.Event.Ref, "preview-message-image/"); ok {
				return a.openMessageImage(ref)
			}
			if id, ok := strings.CutPrefix(e.Event.Ref, "view-image/"); ok {
				return a.openViewedImage(id)
			}
			if index, ok := strings.CutPrefix(e.Event.Ref, "completion/"); ok {
				n, err := strconv.Atoi(index)
				if err == nil && a.completion.kind != 0 && n >= 0 && n < a.completion.count {
					a.completion.selected = n
					return a.handleCompletion(peb.EditorEvent{Kind: "click", Data: "accept"})
				}
				return nil
			}
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
			if id, ok := strings.CutPrefix(e.Event.Ref, "history/"); ok {
				return a.openConversation(id)
			}
			parts := strings.SplitN(e.Event.Ref, "/", 2)
			if c != nil && len(parts) == 2 && (parts[0] == "accept" || parts[0] == "decline") {
				return a.answer(c, parts[1], parts[0])
			}
		}
	}
	return nil
}

// Transcript expansion changes only local view state and can be rendered while
// a background history request is waiting for the server.
func (a *agent) toggleTranscript(ref string) bool {
	c := a.selected
	if c == nil {
		return false
	}
	kind, id, ok := strings.Cut(ref, "/")
	if !ok {
		return false
	}
	switch kind {
	case "turn":
		if p := c.turnProgress(id); p != nil && p.complete {
			p.expanded = !p.expanded
			c.dirty = true
		}
	case "item", "activity":
		for _, i := range c.items {
			if kind == "item" && i.detailRef == ref {
				i.expanded = !i.expanded
				i.html = ""
				c.dirty = true
				break
			}
			if kind == "activity" && i.groupRef == ref {
				i.groupExpanded = !i.groupExpanded
				c.dirty = true
				break
			}
		}
	default:
		return false
	}
	return true
}

func (a *agent) startThread() (*conversation, error) {
	a.selectConversation(nil)
	if err := a.connect(); err != nil {
		return nil, err
	}
	p := a.permission()
	params := struct {
		approvalSettings
		Cwd     string         `json:"cwd"`
		Model   string         `json:"model,omitempty"`
		Sandbox string         `json:"sandbox"`
		Config  questionConfig `json:"config"`
	}{approvalSettings: p.approvalSettings, Cwd: a.context.ProjectPath, Model: a.model, Sandbox: p.sandbox, Config: questionConfig{RequestUserInput: true}}
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
	a.selectConversation(c)
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
	a.dismissCompletion()
	c := a.selected
	prompt := a.drafts[a.context.ProjectPath]
	if c != nil {
		prompt = c.draft
		a.context = c.context
	}
	scope := a.attachmentScope()
	files := a.attachments[scope]
	images := a.images[scope]
	if strings.TrimSpace(prompt) == "" && len(files) == 0 && len(images) == 0 {
		return nil
	}
	if c != nil && c.busy {
		if c.observing {
			return fmt.Errorf("任务正在其他客户端执行，请等待完成或在原客户端停止")
		}
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
		if len(images) > 0 {
			a.images[a.attachmentScope()] = images
			delete(a.images, scope)
		}
		if len(files) > 0 {
			a.attachments[a.attachmentScope()] = files
			delete(a.attachments, scope)
		}
	} else if !c.loaded {
		if err := a.resume(c.ID); err != nil {
			return err
		}
		if !c.loaded {
			return fmt.Errorf("%s", c.status)
		}
	}
	if c.busy {
		return fmt.Errorf("当前会话仍有任务在运行，请等待完成或点击停止")
	}
	c.cancelEdit()
	a.turnInputs = append(a.turnInputs[:0], input{Type: "text", Text: attachmentPrompt(prompt, files)})
	for _, image := range images {
		a.turnInputs = append(a.turnInputs, input{Type: "image", URL: image.url})
	}
	defer func() { clear(a.turnInputs); a.turnInputs = a.turnInputs[:0] }()
	if err := a.startTurn(c, a.turnInputs); err != nil {
		return err
	}
	c.draft = ""
	delete(a.attachments, a.attachmentScope())
	delete(a.images, a.attachmentScope())
	if fresh {
		delete(a.drafts, a.context.ProjectPath)
	}
	if c.Name == "" && c.titleJob == nil {
		a.generateTitle(c, attachmentPrompt(prompt, files))
	}
	return nil
}

func (a *agent) startTurn(c *conversation, inputs []input) error {
	// Show the stop action before waiting for turn/start to acknowledge the task.
	a.setBusy(c, true)
	c.progress = &turnProgress{started: time.Now()}
	c.files = newTurnFiles("", c.context.ProjectPath)
	c.status = ""
	a.flush()
	p := a.permission()
	params := struct {
		approvalSettings
		ThreadID      string        `json:"threadId"`
		Model         string        `json:"model,omitempty"`
		Effort        string        `json:"effort,omitempty"`
		Input         []input       `json:"input"`
		SandboxPolicy sandboxPolicy `json:"sandboxPolicy"`
	}{ThreadID: c.ID, Input: inputs, Model: a.model, Effort: a.turnEffort(), SandboxPolicy: p.policy, approvalSettings: p.approvalSettings}
	result, err := call[struct {
		Turn turn `json:"turn"`
	}](a.client, "turn/start", params)
	if err != nil {
		c.finishProgress(turn{}, time.Now())
		a.setBusy(c, false)
		c.dirty = true
		return err
	}
	if c.files != nil && c.files.id == "" {
		c.files.id, c.files.ref = result.Turn.ID, "changes/"+result.Turn.ID
	}
	a.setBusy(c, c.busy && result.Turn.Status == "inProgress")
	if c.busy {
		c.turnID = result.Turn.ID
		c.startProgress(result.Turn, time.Now())
	} else {
		c.finishProgress(result.Turn, time.Now())
	}
	a.status = ""
	a.workspaceDirty, c.dirty = true, true
	return nil
}

func (a *agent) interrupt(c *conversation) error {
	if c == nil || c.observing || !c.busy || c.turnID == "" || a.client == nil {
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
		SourceKinds    [3]string `json:"sourceKinds"`
		Cwd            string    `json:"cwd"`
		SortKey        string    `json:"sortKey"`
		Cursor         string    `json:"cursor,omitempty"`
		Limit          int       `json:"limit"`
		UseStateDBOnly bool      `json:"useStateDbOnly"`
	}{Cwd: a.context.ProjectPath, Limit: 20, SortKey: "updated_at", Cursor: cursor, SourceKinds: [3]string{"cli", "vscode", "appServer"}, UseStateDBOnly: true})
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
	if err := a.connect(); err != nil {
		return err
	}
	if c == nil || !c.loaded {
		fresh := c == nil
		if fresh {
			// Resume may replay live events and approvals before its response.
			c = &conversation{thread: thread{ID: id}, context: a.context}
			a.conversations[id] = c
		}
		c.items, c.progress, c.files, c.cursor = nil, nil, nil, ""
		resumed := false
		defer func() {
			if fresh && !resumed {
				delete(a.conversations, id)
			}
		}()
		p := a.permission()
		revision := c.turnRevision
		result, err := call[struct {
			Thread thread `json:"thread"`
		}](a.client, "thread/resume", struct {
			approvalSettings
			ThreadID     string         `json:"threadId"`
			Sandbox      string         `json:"sandbox"`
			Config       questionConfig `json:"config"`
			ExcludeTurns bool           `json:"excludeTurns"`
		}{ThreadID: id, ExcludeTurns: true, Sandbox: p.sandbox, Config: questionConfig{RequestUserInput: true}, approvalSettings: p.approvalSettings})
		loaded := err == nil
		if err != nil {
			rpcErr, ok := err.(*rpcError)
			if !ok || rpcErr.Code != -32600 || rpcErr.Message != "thread "+id+" already has an active writer" {
				return err
			}
			// Another app-server owns this thread. History reads do not need
			// its writer lock; retry acquiring it when the user sends again.
			result, err = call[struct {
				Thread thread `json:"thread"`
			}](a.client, "thread/read", struct {
				ThreadID string `json:"threadId"`
			}{id})
			if err != nil {
				return err
			}
		}
		resumed = true
		c.thread = result.Thread
		if c.turnRevision == revision {
			a.setBusy(c, result.Thread.Status.Type == "active")
		}
		c.loaded = loaded
		c.observing = !loaded
		c.syncError, c.syncAfter = "", time.Time{}
		c.observedTurn, c.observedStatus, c.observedDigest = "", "", [32]byte{}
		c.status = ""
		if !loaded {
			c.status = externalSessionStatus
		}
		if err = a.readHistory(c); err != nil {
			a.closeClient()
			return err
		}
		for c.editing != nil && !c.editReverted && !slices.Contains(c.items, c.editing) && c.cursor != "" {
			if err = a.readHistory(c); err != nil {
				return err
			}
		}
		if c.editing != nil && c.editReverted {
			c.items = append(c.items, c.editing)
		}
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
	return a.readHistory(c)
}

// Initial hydration must also work when resume has replayed live items.
func (a *agent) readHistory(c *conversation) error {
	revision := c.turnRevision
	page, err := call[struct {
		NextCursor string `json:"nextCursor"`
		Data       []turn `json:"data"`
	}](a.client, "thread/turns/list", struct {
		ThreadID      string `json:"threadId"`
		SortDirection string `json:"sortDirection"`
		ItemsView     string `json:"itemsView"`
		Cursor        string `json:"cursor,omitempty"`
		Limit         int    `json:"limit"`
	}{ThreadID: c.ID, Limit: 10, SortDirection: "desc", ItemsView: "full", Cursor: c.cursor})
	if err != nil {
		return err
	}
	var older []*chatItem
	slices.Reverse(page.Data)
	if c.observing && c.cursor == "" && len(page.Data) > 0 {
		latest := &page.Data[len(page.Data)-1]
		normalizeObservedTurn(latest)
		c.observedTurn, c.observedStatus = latest.ID, latest.Status
	}
	for _, t := range page.Data {
		files := c.turnFiles(t.ID)
		if files == nil {
			files = newTurnFiles(t.ID, c.context.ProjectPath)
		}
		progress := c.turnProgress(t.ID)
		if progress == nil && (t.DurationMs != nil || t.StartedAt > 0 && t.CompletedAt > 0) {
			progress = &turnProgress{}
			progress.update(t, time.Now())
		}
		if (c.observing || c.loaded && (c.Status.Type == "" || c.Status.Type == "active")) && t.Status == "inProgress" && c.turnRevision == revision {
			a.setBusy(c, true)
			c.turnID = t.ID
			c.startProgress(t, time.Now())
			progress = c.progress
			c.files = files
		}
		for _, item := range t.Items {
			if !slices.ContainsFunc(c.items, func(i *chatItem) bool { return i.data.ID == item.ID }) {
				if !files.complete {
					files.add(item)
				}
				i := newChatItem(item, t.Status != "inProgress")
				i.progress = progress
				i.files = files
				i.setSentAt(t.StartedAt)
				if progress != nil && i.modelText() {
					progress.received = true
				}
				older = append(older, i)
			}
		}
		files.complete = files.complete || t.Status != "inProgress"
	}
	c.items = append(older, c.items...)
	if c.editing != nil {
		for _, i := range c.items {
			if i.data.ID == c.editing.data.ID {
				c.editing = i
				break
			}
		}
	}
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
	sameImage := item.Type == "imageView" && i.data.Type == "imageView" && item.Path == i.data.Path
	i.data, i.complete = item, complete
	i.html = ""
	if item.StartedAtMs > 0 {
		i.sentAt = time.UnixMilli(item.StartedAtMs)
	}
	i.text.Reset()
	i.output.Reset()
	if !sameImage {
		clear(i.images)
		i.images = i.images[:0]
	}
	appendText(&i.text, item.Text)
	for _, part := range item.Content {
		if part.Type == "text" {
			appendText(&i.text, part.Text)
		} else if part.Type == "image" || part.Type == "localImage" {
			attachment := imageAttachment{}
			if preview, err := imagePreview(part.URL, 160); err == nil {
				attachment.url, attachment.preview = part.URL, preview
			}
			i.images = append(i.images, attachment)
		}
	}
	if item.Type == "reasoning" {
		for _, s := range item.Summary {
			appendText(&i.text, s+"\n")
		}
	}
	appendText(&i.output, item.AggregatedOutput)
	// Builders and image attachments own display data; don't retain duplicate payloads.
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
	if message.Method == "skills/changed" {
		a.completion.skillsLoaded = false
		a.completion.skillError = ""
		if a.completion.kind == '/' {
			a.dismissCompletion()
		}
		return
	}
	if message.Method == "thread/status/changed" {
		var event struct {
			ThreadID string       `json:"threadId"`
			Status   threadStatus `json:"status"`
		}
		if err := json.Unmarshal(message.Params, &event); err != nil {
			fmt.Fprintln(stderrWriter, err)
		} else {
			a.updateThreadStatus(event.ThreadID, event.Status)
		}
		return
	}
	var event struct {
		Error       *rpcError       `json:"error"`
		ThreadID    string          `json:"threadId"`
		ItemID      string          `json:"itemId"`
		Delta       string          `json:"delta"`
		ThreadName  string          `json:"threadName"`
		RequestID   json.RawMessage `json:"requestId"`
		Item        threadItem      `json:"item"`
		Turn        turn            `json:"turn"`
		StartedAtMs int64           `json:"startedAtMs"`
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
			r := &approval{id: message.ID, method: message.Method, params: message.Params}
			if message.Method == "item/tool/requestUserInput" {
				questions := &userInputRequest{ref: previewID("question", string(message.ID))}
				if err := json.Unmarshal(message.Params, questions); err != nil || len(questions.Questions) == 0 {
					_ = a.client.reply(message.ID, nil, &rpcError{Code: -32602, Message: "Invalid user input questions"})
					return
				}
				for index := range questions.Questions {
					questions.Questions[index].selected = -1
				}
				r.questions = questions
			}
			c.requests = append(c.requests, r)
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
		if message.Method == "item/completed" && !(i.complete && i.data.Type == "fileChange" && i.data.Status == "completed") && i.files != nil && !i.files.complete {
			i.files.add(event.Item)
		}
		i.replace(event.Item, message.Method == "item/completed")
		if event.StartedAtMs > 0 {
			i.sentAt = time.UnixMilli(event.StartedAtMs)
		}
		i.setSentAt(time.Now().Unix())
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
		a.setBusy(c, true)
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
		if event.Turn.Status == "completed" && (c != a.selected || !a.workspaceOpen || a.workspaceHidden) {
			c.unread, a.historyDirty = true, true
		}
		c.finishProgress(event.Turn, time.Now())
		if c.files != nil && !c.files.complete {
			c.files.complete = true
		}
		c.turnRevision++
		a.setBusy(c, false)
		c.turnID = ""
		c.requests = nil
		if event.Turn.Error != nil {
			c.status = event.Turn.Error.Message
		}
		if event.Turn.Status == "interrupted" {
			c.status = "任务已停止"
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
			answers := make(map[string]userInputAnswer, len(r.questions.Questions))
			if decision != "skip" {
				for index := range r.questions.Questions {
					q := &r.questions.Questions[index]
					text := q.value()
					if text == "" {
						return fmt.Errorf("请回答所有问题")
					}
					answers[q.ID] = userInputAnswer{Answers: []string{text}}
				}
			}
			result = struct {
				Answers map[string]userInputAnswer `json:"answers"`
			}{answers}
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
