package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/onix-engine/onix/html/markup"
	"github.com/pencode-labs/pencode/peb"
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/extension"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/renderer"
	markdownhtml "github.com/yuin/goldmark/v2/renderer/html"
)

//go:embed resources/chat.css
var chatCSS string

// Preview IDs must fit the host's 128-byte action ID alphabet, even when
// their keys contain paths or escaped thread/turn IDs.
func previewID(kind, key string) string {
	sum := sha256.Sum256([]byte(key))
	var encoded [sha256.Size * 2]byte
	hex.Encode(encoded[:], sum[:])
	return kind + "-" + string(encoded[:])
}

// Simple delimiter rules allow emphasis beside CJK punctuation without spaces.
var chatParser = parser.New(parser.WithParseDelimiterFunc(parser.ParseDelimiterSimple), parser.WithExtensions(extension.TableParser, extension.NewLinkifyParser()))
var chatRenderer = markdownhtml.New(markdownhtml.WithHardWraps(), markdownhtml.WithExtensions(extension.TableHTMLRenderer), markdownhtml.WithNodeRendererDecorators(map[ast.NodeKind]markdownhtml.NodeRendererDecorator{
	ast.KindLink: chatLinkDecorator, ast.KindAutoLink: chatLinkDecorator, ast.KindCodeBlock: chatCodeBlockDecorator,
}))

func (a *agent) flush() {
	if a.publisher == nil || !a.dirty() {
		return
	}
	title, busy, unread := "Codex 新会话", false, false
	if a.selected != nil {
		title = a.selected.title()
		busy, unread = a.selected.busy, a.selected.unread
	}
	var view *peb.EditorView
	if a.chatPaneHTML == "" || a.workspaceDirty || a.selected != nil && a.selected.dirty {
		view = a.composerView()
	} else {
		view = a.historyView()
	}
	if err := a.publisher.Show(peb.UI{ID: "composer", Title: title, Kind: peb.EditorTab, Context: a.context, View: view, Busy: busy, Unread: unread}); err != nil {
		fmt.Fprintln(stderrWriter, err)
		return
	}
	if a.revealWorkspace {
		if err := a.publisher.Show(peb.UI{ID: "composer", Title: title, Kind: peb.EditorTab, Context: a.context, Busy: busy, Unread: unread}); err != nil {
			fmt.Fprintln(stderrWriter, err)
			return
		}
		a.revealWorkspace = false
	}
	a.workspaceDirty, a.historyDirty = false, false
	if a.selected != nil {
		a.selected.dirty = false
	}
}

func (a *agent) composerAction() string {
	c := a.selected
	if c == nil {
		return "send"
	}
	if c.busy && c.observing {
		return ""
	}
	scope := a.attachmentScope()
	if strings.TrimSpace(c.draft) != "" || len(a.attachments[scope]) != 0 || len(a.images[scope]) != 0 {
		return "send"
	}
	if c.busy {
		return "stop"
	}
	if c.loaded && c.interrupted {
		return "continue"
	}
	return "send"
}

func (a *agent) composerView() *peb.EditorView {
	b := markup.Acquire()
	defer b.Release()
	bindings := make([]peb.EditorBinding, 0, 16)
	b.Open("main", "class", "codex-workspace", "data-pencode-layout", "fill")
	pane := b.Open("section", "class", "codex-chat-pane")
	key := ""
	if a.selected != nil {
		key = a.selected.ID
	}
	b.Open("div", "class", "codex-transcript", "data-ref", "transcript", "data-pencode-selectable", "", "data-pencode-scroll", "end", "data-pencode-scroll-key", key)
	status := a.status
	if a.selected != nil && a.selected.status != "" {
		status = a.selected.status
	}
	if a.selected != nil && a.selected.syncError != "" {
		status = a.selected.syncError
	}
	if a.selected != nil {
		bindings = a.selected.render(b, bindings, status, a.sending)
	} else {
		empty := conversation{context: a.context}
		bindings = empty.render(b, bindings, status, a.sending)
	}
	b.End()
	p := a.permission()
	draft, title := a.drafts[a.context.ProjectPath], "新会话"
	busy := false
	if a.selected != nil {
		draft, title, busy = a.selected.draft, a.selected.title(), a.selected.busy
	}
	b.Open("section", "class", "codex-composer")
	if a.selected != nil {
		if q := a.selected.question(""); q != nil {
			bindings = q.render(b, bindings)
		}
	}
	b.Open("div", "data-ref", "live-changes-region", "data-pencode-region", "")
	if busy && a.selected.files != nil && !a.selected.files.complete {
		a.selected.files.prepareLive()
		b.Embed(a.selected.files.liveHTML)
	}
	b.End()
	b.Open("form", "id", "pencode-extension-codex-form", "data-ref", "form", "data-pencode-focus", "prompt", "class", "codex-input-card", "aria-label", title)
	if images := a.images[a.attachmentScope()]; len(images) > 0 {
		b.Open("div", "class", "codex-images", "aria-label", "附加图片")
		for index, image := range images {
			previewRef := "preview-image/" + strconv.Itoa(index)
			removeRef := "remove-image/" + strconv.Itoa(index)
			b.Open("div", "class", "codex-image")
			b.Open("button", "class", "codex-image-open", "type", "button", "data-ref", previewRef, "title", "预览图片", "aria-haspopup", "dialog", "aria-label", "预览图片 "+strconv.Itoa(index+1))
			b.Element("img", "src", image.preview, "alt", "待发送图片")
			b.End()
			b.Open("button", "class", "codex-icon-button codex-image-remove", "type", "button", "data-ref", removeRef, "title", "移除图片", "aria-label", "移除图片 "+strconv.Itoa(index+1))
			b.Element("img", "id", "pencode-extension-codex-remove-image-icon-"+strconv.Itoa(index), "src", "resources/icons/x.svg", "alt", "")
			b.End()
			b.End()
			bindings = append(bindings, peb.EditorBinding{Ref: previewRef, Kind: "click"}, peb.EditorBinding{Ref: removeRef, Kind: "click"})
		}
		b.End()
	}
	files := a.attachments[a.attachmentScope()]
	if len(files) > 0 {
		b.Open("div", "class", "codex-attachments", "aria-label", "附加文件")
		for index, path := range files {
			ref := "remove-file/" + strconv.Itoa(index)
			b.Open("button", "class", "codex-attachment", "type", "button", "data-ref", ref, "title", path, "aria-label", "移除文件 "+path)
			b.TextElement("span", filepath.Base(path))
			b.TextElement("span", "×", "aria-hidden", "true")
			b.End()
			bindings = append(bindings, peb.EditorBinding{Ref: ref, Kind: "click"})
		}
		b.End()
	}
	prompt := b.TextElement("textarea", draft, "id", "pencode-extension-codex-prompt", "data-ref", "prompt", "data-pencode-markdown-links", "", "data-pencode-autosize", "", "data-pencode-paste-images", "", "data-pencode-submit-on-enter", "form", "class", "codex-prompt", "aria-label", "给 Codex 的任务", "placeholder", "和 Codex 一起工作，/ 选择技能，@ 引用文件或文件夹", "rows", "3")
	if a.completion.kind != 0 {
		prompt.SetAttr("data-pencode-completion", "true")
		prompt.SetAttr("aria-expanded", "true")
		prompt.SetAttr("aria-controls", "pencode-extension-codex-completion-list")
		prompt.SetAttr("aria-autocomplete", "list")
		if a.completion.count != 0 {
			prompt.SetAttr("aria-activedescendant", "pencode-extension-codex-completion-option-"+strconv.Itoa(a.completion.selected))
		}
	}
	if a.completion.revision != 0 {
		prompt.SetAttr("data-pencode-caret", strconv.Itoa(a.completion.caret)+":"+strconv.FormatUint(a.completion.revision, 10))
	}
	b.Open("div", "class", "codex-input-footer")
	b.Open("button", "data-ref", "add-file", "class", "codex-icon-button", "type", "button", "title", "添加文件", "aria-label", "添加文件", "aria-haspopup", "dialog")
	b.Element("img", "src", "resources/icons/plus.svg", "alt", "")
	b.End()
	class, label := "codex-access", p.label
	if p.sandbox == "danger-full-access" {
		class += " codex-access-full"
		label = "完全访问"
	}
	b.Open("button", "data-ref", "permission-settings", "class", class, "type", "button", "aria-haspopup", "menu", "aria-label", "权限设置", "aria-expanded", strconv.FormatBool(a.permissionSettings), "title", p.title)
	b.Element("img", "src", p.icon, "alt", "")
	b.TextElement("span", label)
	b.End()
	b.Element("span", "class", "codex-input-spacer")
	b.Open("button", "data-ref", "model-settings", "class", "codex-model-select", "type", "button", "aria-haspopup", "menu", "title", "选择模型", "aria-label", "选择模型", "aria-expanded", strconv.FormatBool(a.modelSettings))
	b.TextElement("span", a.modelLabel())
	if a.effort != "" {
		b.TextElement("span", effortLabel(a.effort), "class", "codex-model-effort")
	}
	b.Element("img", "src", "resources/icons/chevron-down.svg", "alt", "")
	b.End()
	action := a.composerAction()
	switch action {
	case "":
		b.Open("button", "class", "codex-icon-button codex-send", "type", "button", "disabled", "", "title", "任务正在其他客户端执行，请在原客户端停止", "aria-label", "正在其他客户端执行")
		b.Element("img", "src", "resources/icons/square.svg", "alt", "")
		b.End()
	case "stop":
		b.Open("button", "data-ref", "stop", "class", "codex-icon-button codex-send", "type", "button", "title", "停止任务", "aria-label", "停止任务")
		b.Element("img", "src", "resources/icons/square.svg", "alt", "")
		b.End()
	case "continue":
		b.Open("button", "data-ref", "continue", "class", "codex-icon-button codex-send", "type", "button", "title", "继续任务", "aria-label", "继续任务")
		b.Element("img", "src", "resources/icons/play.svg", "alt", "")
		b.End()
	case "send":
		label := "发送"
		if busy {
			label = "追加消息"
		}
		b.Open("button", "data-ref", "send", "class", "codex-icon-button codex-send", "type", "button", "title", label, "aria-label", label)
		b.Element("img", "src", "resources/icons/arrow-up.svg", "alt", "")
		b.End()
	}
	b.End()
	b.End()
	bindings = a.renderCompletion(b, bindings)
	bindings = append(bindings, peb.EditorBinding{Ref: "prompt", Kind: "input"}, peb.EditorBinding{Ref: "form", Kind: "submit"}, peb.EditorBinding{Ref: "add-file", Kind: "click"}, peb.EditorBinding{Ref: "permission-settings", Kind: "click"}, peb.EditorBinding{Ref: "model-settings", Kind: "click"})
	if action != "" {
		bindings = append(bindings, peb.EditorBinding{Ref: action, Kind: "click"})
	}
	b.End()
	b.End()
	bindings = append(bindings, peb.EditorBinding{Ref: "new", Kind: "click"}, peb.EditorBinding{Ref: "refresh-history", Kind: "click"})
	a.composerBindings = bindings
	bindings = a.renderHistory(b, bindings)
	b.End()
	document, chatPane := b.StringWithSubtree(pane)
	a.chatPaneHTML = chatPane
	return &peb.EditorView{HTML: document, CSS: chatCSS, Events: bindings}
}

func (a *agent) historyView() *peb.EditorView {
	b := markup.Acquire()
	defer b.Release()
	b.Open("main", "class", "codex-workspace", "data-pencode-layout", "fill")
	b.Embed(a.chatPaneHTML)
	bindings := make([]peb.EditorBinding, len(a.composerBindings), len(a.composerBindings)+2*len(a.history.Data)+1)
	copy(bindings, a.composerBindings)
	bindings = a.renderHistory(b, bindings)
	b.End()
	return &peb.EditorView{HTML: b.String(), CSS: chatCSS, Events: bindings}
}

func (a *agent) renderHistory(b *markup.Builder, bindings []peb.EditorBinding) []peb.EditorBinding {
	b.Open("aside", "class", "codex-sidebar", "aria-label", "历史会话")
	b.Open("section", "class", "codex-history")

	b.Open("div", "class", "codex-history-header", "data-ref", "history-header", "data-pencode-region", "")
	b.TextElement("h3", "当前项目的会话")
	b.Open("button", "data-ref", "new", "class", "codex-icon-button", "type", "button", "title", "新会话", "aria-label", "新会话")
	b.Element("img", "data-ref", "new-icon", "data-pencode-icon", "add", "alt", "")
	b.End()
	label := "刷新会话"
	if a.historyLoading {
		label = "正在加载会话"
	}
	refresh := b.Open("button", "data-ref", "refresh-history", "class", "codex-icon-button", "type", "button", "title", label, "aria-label", label)
	icon := b.Element("img", "data-ref", "refresh-history-icon", "data-pencode-icon", "refresh-cw", "alt", "")
	if a.historyLoading {
		refresh.SetAttr("disabled", "")
		refresh.SetAttr("aria-busy", "true")
		icon.SetAttr("class", "codex-history-spinning")
	}
	b.End()
	b.End()
	b.Open("div", "class", "codex-history-list", "data-ref", "history-list", "data-pencode-region", "", "data-pencode-scroll", "preserve", "data-pencode-scroll-key", a.context.ProjectPath)
	if a.showHistory {
		if len(a.history.Data) == 0 {
			b.TextElement("p", "暂无历史会话。发送第一条消息开始工作。", "class", "codex-muted")
		}
		for _, t := range a.history.Data {
			ref := "history/" + t.ID
			b.Open("div", "class", "codex-history-entry")
			b.Open("button", "class", "codex-history-row", "type", "button", "data-ref", ref, "aria-current", strconv.FormatBool(a.selected != nil && a.selected.ID == t.ID))
			title := t.Name
			if title == "" {
				title = shortTitle(t.Preview)
			}
			b.Text(title)
			b.End()
			renameRef := "rename/" + t.ID
			b.Open("div", "class", "codex-history-actions")
			b.Open("button", "class", "codex-icon-button", "type", "button", "data-ref", renameRef, "title", "修改会话标题", "aria-label", "修改会话标题", "aria-haspopup", "dialog")
			b.Element("img", "src", "resources/icons/pencil.svg", "alt", "")
			b.End()
			c := a.conversations[t.ID]
			if c != nil && c.busy || c == nil && t.Status.Type == "active" {
				b.Element("img", "class", "codex-history-running", "src", "resources/icons/loader-circle.svg", "role", "status", "aria-label", "会话运行中", "title", "会话运行中", "alt", "")
			} else if c != nil && c.unread {
				b.Element("span", "class", "codex-history-unread", "role", "status", "aria-label", "会话已完成，尚未查看", "title", "会话已完成，尚未查看")
			}
			b.End()
			b.End()
			bindings = append(bindings, peb.EditorBinding{Ref: ref, Kind: "click"}, peb.EditorBinding{Ref: renameRef, Kind: "click"})
		}
		if a.history.NextCursor != "" {
			more := b.TextElement("button", "更多会话", "data-ref", "more-history", "type", "button")
			if a.historyLoading {
				more.SetAttr("disabled", "")
			}
			bindings = append(bindings, peb.EditorBinding{Ref: "more-history", Kind: "click"})
		}
	} else if a.historyLoading {
		b.TextElement("p", "正在加载历史会话…", "class", "codex-muted", "role", "status")
	} else {
		b.TextElement("p", "点击刷新，查看当前项目的历史会话。", "class", "codex-muted")
	}
	b.End()
	b.End()
	b.End()
	return bindings
}

func renderChatStatus(b *markup.Builder, status string) {
	if status != "" {
		b.TextElement("p", status, "class", "codex-message codex-status", "role", "status")
	}
}

func (c *conversation) renderProgress(b *markup.Builder, bindings []peb.EditorBinding, progress *turnProgress, status string, collapsible bool) []peb.EditorBinding {
	progress.render(b, collapsible)
	if collapsible {
		bindings = append(bindings, peb.EditorBinding{Ref: progress.toggleRef, Kind: "click"})
	}
	if progress == c.progress {
		if status == "" {
			status = progress.retryStatus
		}
		renderChatStatus(b, status)
	}
	return bindings
}

func (c *conversation) render(b *markup.Builder, bindings []peb.EditorBinding, status string, sending *chatItem) []peb.EditorBinding {
	class := "codex-chat"
	if c.busy {
		class += " codex-chat-running"
	}
	b.Open("article", "class", class)
	if c.cursor != "" {
		b.TextElement("button", "加载更早的记录", "data-ref", "older", "type", "button")
		bindings = append(bindings, peb.EditorBinding{Ref: "older", Kind: "click"})
	}
	if len(c.items) == 0 && c.progress == nil && sending == nil {
		b.Open("div", "class", "codex-empty")
		b.TextElement("h2", "与 Codex 一起工作")
		b.TextElement("p", "回复、命令执行和文件修改会显示在这里。")
		b.End()
	}
	latestActivity := -1
	if c.busy && len(c.requests) == 0 {
		for index := len(c.items) - 1; index >= 0; index-- {
			if c.items[index].data.Type == "userMessage" {
				break
			}
			if c.items[index].activityKinds() != 0 {
				latestActivity = index
				break
			}
		}
	}
	// Render only changed items. Reserve room for approvals and keep the latest
	// messages visible even when an old command produced a large amount of output.
	start, size := len(c.items), 0
	var files *turnFiles
	for start > 0 {
		i := c.items[start-1]
		if sweep := start-1 == latestActivity; i.sweep != sweep {
			i.sweep = sweep
			i.html = ""
		}
		if i.files != files {
			files = i.files
			files.prepare()
			if files != nil {
				size += len(files.html)
			}
		}
		if i.html == "" && i.data.Type != "imageView" {
			fragment := markup.Acquire()
			i.render(fragment, c.context.ProjectPath, fmt.Sprintf("%p", i))
			i.html = fragment.String()
			fragment.Release()
			if len(i.html) > 512<<10 {
				var notice markup.Builder
				notice.TextElement("p", "此条输出过长，请在 Codex 历史中查看完整内容。", "class", "codex-muted")
				i.html = notice.String()
				i.detailRef = ""
				i.linkBindings = nil
			}
		}
		if size+len(i.html) > 1200<<10 {
			break
		}
		size += len(i.html)
		start--
	}
	if start > 0 {
		// Keep one boundary item warm so a streaming update does not repeatedly
		// render the same large message just outside the visible byte budget.
		c.releasePaintBefore(start - 1)
		b.TextElement("p", "较早输出已折叠，完整记录保存在 Codex 会话中。", "class", "codex-muted")
	}
	renderedProgress := false
	files = nil
	for index := start; index < len(c.items); {
		i := c.items[index]
		if i.files != files {
			bindings = files.render(b, bindings)
			files = i.files
		}
		end := index + 1
		for end < len(c.items) && c.items[end].progress == i.progress && c.items[end].files == i.files {
			end++
		}
		bindings = c.renderTurn(b, bindings, c.items[index:end], status, latestActivity-index)
		renderedProgress = renderedProgress || i.progress == c.progress
		index = end
	}
	bindings = files.render(b, bindings)
	if c.progress != nil && !renderedProgress {
		bindings = c.renderProgress(b, bindings, c.progress, status, false)
	}
	if c.progress == nil {
		renderChatStatus(b, status)
	}
	for _, r := range c.requests {
		if r.questions == nil {
			bindings = r.render(b, bindings)
		}
	}
	if sending != nil {
		sending.render(b, c.context.ProjectPath, fmt.Sprintf("%p", sending))
		bindings = append(bindings, sending.linkBindings...)
	}
	b.End()
	return bindings
}

func (c *conversation) renderTurn(b *markup.Builder, bindings []peb.EditorBinding, items []*chatItem, status string, latestActivity int) []peb.EditorBinding {
	p := items[0].progress
	if p == nil {
		return c.renderItems(b, bindings, items, latestActivity)
	}
	// During a steer, put progress below the latest input, even before its echo.
	// Completed turns keep their original prompt outside the collapsible region.
	start := 0
	for start < len(items) && (items[start].data.Type == "userMessage" || !p.complete && items[start].liveOrder < p.followupFrom) {
		start++
	}
	bindings = c.renderItems(b, bindings, items[:start], latestActivity)
	if !p.complete {
		bindings = c.renderProgress(b, bindings, p, status, false)
		return c.renderItems(b, bindings, items[start:], latestActivity-start)
	}
	final := len(items)
	for index := len(items) - 1; index >= start; index-- {
		i := items[index]
		if i.data.Type == "agentMessage" && i.data.Phase != "commentary" && i.text.Len() > 0 {
			final = index
			break
		}
	}
	collapsible := p.id != "" && (final > start || final+1 < len(items))
	turn := b.Open("section", "class", "codex-turn")
	if p.id != "" {
		turn.SetAttr("data-ref", "turn/"+p.id+"/region")
		turn.SetAttr("data-pencode-region", "")
	}
	bindings = c.renderProgress(b, bindings, p, status, collapsible)
	if !collapsible || p.expanded {
		bindings = c.renderItems(b, bindings, items[start:final], latestActivity-start)
		if final < len(items) {
			bindings = c.renderItems(b, bindings, items[final+1:], latestActivity-final-1)
		}
	}
	b.End()
	if final < len(items) {
		bindings = c.renderItems(b, bindings, items[final:final+1], -1)
	}
	return bindings
}

func (c *conversation) renderItems(b *markup.Builder, bindings []peb.EditorBinding, items []*chatItem, latestActivity int) []peb.EditorBinding {
	for index := 0; index < len(items); {
		i := items[index]
		if i.data.Type == "imageView" {
			end := index + 1
			for end < len(items) && items[end].data.Type == "imageView" {
				end++
			}
			bindings = renderViewedImages(b, bindings, items[index:end], c.context.ProjectPath)
			index = end
			continue
		}
		if i.activityKinds() != 0 {
			end := index + 1
			for end < len(items) && items[end].activityKinds() != 0 {
				end++
			}
			bindings = renderActivityGroup(b, bindings, items[index:end], latestActivity >= index && latestActivity < end)
			index = end
			continue
		}
		if c.editing == i {
			bindings = c.renderMessageEditor(b, bindings)
		} else {
			b.Embed(i.html)
			bindings = append(bindings, i.linkBindings...)
		}
		if i.detailRef != "" {
			bindings = append(bindings, peb.EditorBinding{Ref: i.detailRef, Kind: "click"})
		}
		index++
	}
	return bindings
}

func (i *chatItem) render(b *markup.Builder, root, scope string) {
	i.linkBindings = i.linkBindings[:0]
	if i.questions != nil && !i.questions.answered {
		return
	}
	label, icon := "", "wrench"
	class := "codex-message"
	switch i.data.Type {
	case "userMessage":
		label = "你"
		class = "codex-message codex-user"
	case "agentMessage":
		label = "Codex"
	case "plan":
		label = "计划"
	case "reasoning":
		label = i.reasoningLabel()
	case "commandExecution":
		label, icon = "运行了命令", "terminal"
	case "fileChange":
		label, icon = "编辑了文件", "pencil"
	case "mcpToolCall", "dynamicToolCall", "collabToolCall":
		label = "调用了工具 " + strings.TrimSpace(i.data.Server+" "+i.data.Tool)
	case "webSearch":
		label, icon = "搜索了 "+i.data.Query, "search"
	case "contextCompaction":
		label = "上下文已压缩"
	default:
		i.detailRef = ""
		return
	}
	if kinds := i.activityKinds(); kinds != 0 {
		icon = kinds.primary().icon
	}
	activity := i.data.Type != "userMessage" && i.data.Type != "agentMessage"
	if activity {
		class = "codex-message codex-activity"
	} else {
		i.detailRef = ""
	}
	section := b.Open("section", "class", class, "aria-label", label)
	if i.data.ID != "" {
		section.SetAttr("data-ref", "message/"+i.data.ID)
		section.SetAttr("data-pencode-region", "")
	}
	if i.data.Type == "userMessage" {
		i.renderImages(b)
		if i.text.Len() > 0 {
			b.Open("div", "class", "codex-user-bubble")
		}
	}
	if activity {
		hasDetails := i.text.Len() > 0 || i.output.Len() > 0 || i.data.Command != "" || len(i.data.Changes) > 0
		if i.data.ID != "" && hasDetails {
			if i.detailRef == "" {
				i.detailRef = "item/" + i.data.ID
			}
			b.Open("button", "class", "codex-activity-summary", "type", "button", "data-ref", i.detailRef, "aria-expanded", strconv.FormatBool(i.expanded))
		} else {
			i.detailRef = ""
			b.Open("div", "class", "codex-activity-summary")
		}
		b.Element("img", "src", "resources/icons/"+icon+".svg", "alt", "")
		b.Open("span")
		var text strings.Builder
		i.renderActivityLabel(&text, label)
		if i.sweep {
			renderSweepText(b, text.String())
		} else {
			b.Text(text.String())
		}
		b.End()
		renderActivityStatus(b, i.data.Status)
		if i.detailRef != "" {
			b.Element("img", "class", "codex-activity-chevron", "src", "resources/icons/chevron-down.svg", "alt", "")
		}
		b.End()
		if !hasDetails || i.detailRef != "" && !i.expanded {
			b.End()
			return
		}
		b.Open("div", "class", "codex-activity-details")
	}
	if i.text.Len() > 0 && (i.data.Type != "userMessage" || !renderQuestionReplies(b, i.text.String())) {
		source := i.text.String()
		markdown := i.data.Type == "userMessage" || i.data.Type == "agentMessage" || i.data.Type == "plan"
		if markdown && (i.complete || strings.Contains(source, "```") || strings.Contains(source, "~~~")) {
			state := chatMarkdownState{root: root, scope: scope, wrapped: i.wrappedCode, bindings: i.linkBindings}
			i.linkBindings = renderMarkdown(b, source, state)
		} else {
			b.TextElement("div", i.text.String(), "class", "codex-text", "data-pencode-preserve-whitespace", "")
		}
	}
	if i.data.Command != "" {
		pre(b, i.data.Command)
	}
	if i.output.Len() > 0 {
		pre(b, i.output.String())
	}
	for _, change := range i.data.Changes {
		b.TextElement("h4", change.Path)
		renderDiff(b, change.Diff[:min(len(change.Diff), 64<<10)], change.Kind.Type)
	}
	if activity {
		b.End()
	}
	if i.data.Type == "userMessage" {
		if i.text.Len() > 0 {
			b.End()
		}
		if i.sendError != "" {
			b.TextElement("p", "发送失败 · "+i.sendError, "class", "codex-send-error codex-status", "role", "alert")
		}
		i.renderMessageActions(b)
	}
	b.End()
}

func pre(b *markup.Builder, text string) {
	b.Open("pre")
	b.TextElement("code", text)
	b.End()
}

var chatMarkdownContext = renderer.NewContextKey()

type chatMarkdownState struct {
	wrapped              map[string]bool
	root, scope          string
	bindings             []peb.EditorBinding
	linkIndex, codeIndex int
}

func renderMarkdown(b *markup.Builder, source string, state chatMarkdownState) []peb.EditorBinding {
	document := chatParser.ParseStringSource(source)
	// Chat text must not initiate resource loads or leak the task in image URLs.
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if _, ok := node.(*ast.Image); entering && ok {
			node.Parent().RemoveChild(node)
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	context := renderer.NewContext()
	context.Set(chatMarkdownContext, &state)
	var rendered strings.Builder
	if err := chatRenderer.RenderStringSource(&rendered, source, document, renderer.WithContext(context)); err != nil {
		pre(b, source)
	} else {
		b.Embed(rendered.String())
	}
	return state.bindings
}

func (r *approval) render(b *markup.Builder, bindings []peb.EditorBinding) []peb.EditorBinding {
	b.Open("section", "class", "codex-approval")
	b.TextElement("h3", "Codex 需要你的确认")
	id := string(r.id)
	var formatted bytes.Buffer
	_ = json.Indent(&formatted, r.params, "", "  ")
	pre(b, formatted.String())
	bindings = actionButton(b, bindings, "accept/"+id, "允许本次操作")
	bindings = actionButton(b, bindings, "decline/"+id, "拒绝")
	b.End()
	return bindings
}

func actionButton(b *markup.Builder, bindings []peb.EditorBinding, ref, label string) []peb.EditorBinding {
	b.TextElement("button", label, "type", "button", "data-ref", ref)
	return append(bindings, peb.EditorBinding{Ref: ref, Kind: "click"})
}
