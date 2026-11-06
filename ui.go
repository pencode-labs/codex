package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"path/filepath"
	"strconv"
	"strings"

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
	ast.KindLink: chatLinkDecorator, ast.KindAutoLink: chatLinkDecorator,
}))

func (a *agent) flush() {
	if !a.dirty() {
		return
	}
	title, busy, unread := "Codex 新会话", false, false
	if a.selected != nil {
		title = a.selected.title()
		busy, unread = a.selected.busy, a.selected.unread
	}
	var view *peb.EditorView
	if a.composerPrefix == "" || a.workspaceDirty || a.selected != nil && a.selected.dirty {
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

func (a *agent) composerView() *peb.EditorView {
	var b strings.Builder
	bindings := make([]peb.EditorBinding, 0, 16)
	b.WriteString(`<main class="codex-workspace" data-pencode-layout="fill"><section class="codex-chat-pane"><div class="codex-transcript" data-ref="transcript" data-pencode-selectable data-pencode-scroll="end" data-pencode-scroll-key="`)
	if a.selected != nil {
		b.WriteString(html.EscapeString(a.selected.ID))
	}
	b.WriteString(`">`)
	status := a.status
	if a.selected != nil && a.selected.status != "" {
		status = a.selected.status
	}
	if a.selected != nil && a.selected.syncError != "" {
		status = a.selected.syncError
	}
	if a.selected != nil {
		bindings = a.selected.render(&b, bindings, status)
	} else {
		b.WriteString(`<article class="codex-chat">`)
		renderChatStatus(&b, status)
		b.WriteString(`<div class="codex-empty"><h2>与 Codex 一起工作</h2><p>回复、命令执行和文件修改会显示在这里。</p></div></article>`)
	}
	b.WriteString(`</div>`)
	p := a.permission()
	draft, title := a.drafts[a.context.ProjectPath], "新会话"
	busy := false
	if a.selected != nil {
		draft, title, busy = a.selected.draft, a.selected.title(), a.selected.busy
	}
	b.WriteString(`<section class="codex-composer">`)
	if a.selected != nil {
		for _, r := range a.selected.requests {
			if r.questions != nil {
				bindings = r.questions.render(&b, bindings)
				break
			}
		}
	}
	if busy && a.selected.files != nil && !a.selected.files.complete {
		a.selected.files.prepareLive()
		b.WriteString(a.selected.files.liveHTML)
		bindings = append(bindings, a.selected.files.liveBindings...)
	}
	b.WriteString(`<form id="pencode-extension-codex-form" data-ref="form" data-pencode-focus="prompt" class="codex-input-card" aria-label="`)
	b.WriteString(html.EscapeString(title))
	b.WriteString(`">`)
	if images := a.images[a.attachmentScope()]; len(images) > 0 {
		b.WriteString(`<div class="codex-images" aria-label="附加图片">`)
		for index, image := range images {
			previewRef := "preview-image/" + strconv.Itoa(index)
			removeRef := "remove-image/" + strconv.Itoa(index)
			b.WriteString(`<div class="codex-image"><button class="codex-image-open" type="button" data-ref="`)
			b.WriteString(previewRef)
			b.WriteString(`" title="预览图片" aria-haspopup="dialog" aria-label="预览图片 `)
			b.WriteString(strconv.Itoa(index + 1))
			b.WriteString(`"><img src="`)
			b.WriteString(image.preview)
			b.WriteString(`" alt="待发送图片"></button><button class="codex-icon-button codex-image-remove" type="button" data-ref="`)
			b.WriteString(removeRef)
			b.WriteString(`" title="移除图片" aria-label="移除图片 `)
			b.WriteString(strconv.Itoa(index + 1))
			b.WriteString(`"><img id="pencode-extension-codex-remove-image-icon-`)
			b.WriteString(strconv.Itoa(index))
			b.WriteString(`" src="resources/icons/x.svg" alt=""></button></div>`)
			bindings = append(bindings, peb.EditorBinding{Ref: previewRef, Kind: "click"}, peb.EditorBinding{Ref: removeRef, Kind: "click"})
		}
		b.WriteString(`</div>`)
	}
	files := a.attachments[a.attachmentScope()]
	if len(files) > 0 {
		b.WriteString(`<div class="codex-attachments" aria-label="附加文件">`)
		for index, path := range files {
			ref := "remove-file/" + strconv.Itoa(index)
			b.WriteString(`<button class="codex-attachment" type="button" data-ref="`)
			b.WriteString(ref)
			b.WriteString(`" title="`)
			b.WriteString(html.EscapeString(path))
			b.WriteString(`" aria-label="移除文件 `)
			b.WriteString(html.EscapeString(path))
			b.WriteString(`"><span>`)
			b.WriteString(html.EscapeString(filepath.Base(path)))
			b.WriteString(`</span><span aria-hidden="true">×</span></button>`)
			bindings = append(bindings, peb.EditorBinding{Ref: ref, Kind: "click"})
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`<textarea id="pencode-extension-codex-prompt" data-ref="prompt" data-pencode-markdown-links data-pencode-autosize data-pencode-paste-images data-pencode-submit-on-enter="form"`)
	if a.completion.kind != 0 {
		b.WriteString(` data-pencode-completion="true" aria-expanded="true" aria-controls="pencode-extension-codex-completion-list" aria-autocomplete="list"`)
		if a.completion.count != 0 {
			b.WriteString(` aria-activedescendant="pencode-extension-codex-completion-option-`)
			b.WriteString(strconv.Itoa(a.completion.selected))
			b.WriteByte('"')
		}
	}
	if a.completion.revision != 0 {
		b.WriteString(` data-pencode-caret="`)
		b.WriteString(strconv.Itoa(a.completion.caret))
		b.WriteByte(':')
		b.WriteString(strconv.FormatUint(a.completion.revision, 10))
		b.WriteByte('"')
	}
	b.WriteString(` class="codex-prompt" aria-label="给 Codex 的任务" placeholder="和 Codex 一起工作，/ 选择技能，@ 引用文件或文件夹" rows="2">`)
	b.WriteString(html.EscapeString(draft))
	b.WriteString(`</textarea><div class="codex-input-footer"><button data-ref="add-file" class="codex-icon-button" type="button" title="添加文件" aria-label="添加文件" aria-haspopup="dialog"><img src="resources/icons/plus.svg" alt=""></button><button data-ref="permission-settings" class="codex-access`)
	if p.sandbox == "danger-full-access" {
		b.WriteString(` codex-access-full`)
	}
	b.WriteString(`" type="button" aria-haspopup="menu" aria-label="权限设置" aria-expanded="`)
	b.WriteString(strconv.FormatBool(a.permissionSettings))
	b.WriteString(`" title="`)
	b.WriteString(p.description)
	b.WriteString(`；从下一次发送开始生效"><img src="resources/icons/`)
	b.WriteString(p.icon)
	b.WriteString(`.svg" alt=""><span>`)
	if p.sandbox == "danger-full-access" {
		b.WriteString("完全访问")
	} else {
		b.WriteString(p.label)
	}
	b.WriteString(`</span></button><span class="codex-input-spacer"></span><button data-ref="model-settings" class="codex-model-select" type="button" aria-haspopup="menu" title="选择模型" aria-label="选择模型" aria-expanded="`)
	b.WriteString(strconv.FormatBool(a.modelSettings))
	b.WriteString(`"><span>`)
	b.WriteString(html.EscapeString(a.modelLabel()))
	b.WriteString(`</span>`)
	if a.effort != "" {
		b.WriteString(`<span class="codex-model-effort">`)
		b.WriteString(html.EscapeString(effortLabel(a.effort)))
		b.WriteString(`</span>`)
	}
	b.WriteString(`<img src="resources/icons/chevron-down.svg" alt=""></button>`)
	if busy && a.selected.observing {
		b.WriteString(`<button class="codex-icon-button codex-send" type="button" disabled title="任务正在其他客户端执行，请在原客户端停止" aria-label="正在其他客户端执行"><img src="resources/icons/square.svg" alt=""></button>`)
	} else if busy {
		b.WriteString(`<button data-ref="stop" class="codex-icon-button codex-send" type="button" title="停止任务" aria-label="停止任务"><img src="resources/icons/square.svg" alt=""></button>`)
	} else {
		b.WriteString(`<button data-ref="send" class="codex-icon-button codex-send" type="button" title="发送" aria-label="发送"><img src="resources/icons/arrow-up.svg" alt=""></button>`)
	}
	b.WriteString(`</div></form>`)
	bindings = a.renderCompletion(&b, bindings)
	bindings = append(bindings, peb.EditorBinding{Ref: "prompt", Kind: "input"}, peb.EditorBinding{Ref: "form", Kind: "submit"}, peb.EditorBinding{Ref: "add-file", Kind: "click"}, peb.EditorBinding{Ref: "permission-settings", Kind: "click"}, peb.EditorBinding{Ref: "model-settings", Kind: "click"})
	if busy && !a.selected.observing {
		bindings = append(bindings, peb.EditorBinding{Ref: "stop", Kind: "click"})
	} else if !busy {
		bindings = append(bindings, peb.EditorBinding{Ref: "send", Kind: "click"})
	}
	b.WriteString(`</section></section><aside class="codex-sidebar" aria-label="历史会话"><section class="codex-history">`)
	bindings = append(bindings, peb.EditorBinding{Ref: "new", Kind: "click"}, peb.EditorBinding{Ref: "refresh-history", Kind: "click"})
	a.composerPrefix = b.String()
	a.composerBindings = bindings
	return a.historyView()
}

func (a *agent) historyView() *peb.EditorView {
	var b strings.Builder
	b.WriteString(a.composerPrefix)
	bindings := make([]peb.EditorBinding, len(a.composerBindings), len(a.composerBindings)+2*len(a.history.Data)+1)
	copy(bindings, a.composerBindings)
	b.WriteString(`<div class="codex-history-header" data-ref="history-header" data-pencode-region><h3>当前项目的会话</h3><button data-ref="new" class="codex-icon-button" type="button" title="新会话" aria-label="新会话"><img data-ref="new-icon" data-pencode-icon="add" alt=""></button><button data-ref="refresh-history" class="codex-icon-button" type="button"`)
	if a.historyLoading {
		b.WriteString(` disabled aria-busy="true" title="正在加载会话" aria-label="正在加载会话"><img class="codex-history-spinning"`)
	} else {
		b.WriteString(` title="刷新会话" aria-label="刷新会话"><img`)
	}
	b.WriteString(` data-ref="refresh-history-icon" data-pencode-icon="refresh-cw" alt=""></button></div><div class="codex-history-list" data-ref="history-list" data-pencode-region data-pencode-scroll="preserve" data-pencode-scroll-key="`)
	b.WriteString(html.EscapeString(a.context.ProjectPath))
	b.WriteString(`">`)
	if a.showHistory {
		if len(a.history.Data) == 0 {
			b.WriteString(`<p class="codex-muted">暂无历史会话。发送第一条消息开始工作。</p>`)
		}
		for _, t := range a.history.Data {
			ref := "history/" + t.ID
			b.WriteString(`<div class="codex-history-entry"><button class="codex-history-row" type="button" data-ref="`)
			b.WriteString(html.EscapeString(ref))
			b.WriteString(`" aria-current="`)
			b.WriteString(strconv.FormatBool(a.selected != nil && a.selected.ID == t.ID))
			b.WriteString(`">`)
			title := t.Name
			if title == "" {
				title = shortTitle(t.Preview)
			}
			b.WriteString(html.EscapeString(title))
			b.WriteString(`</button>`)
			renameRef := "rename/" + t.ID
			b.WriteString(`<div class="codex-history-actions">`)
			c := a.conversations[t.ID]
			if c != nil && c.busy || c == nil && t.Status.Type == "active" {
				b.WriteString(`<img class="codex-history-running" src="resources/icons/loader-circle.svg" role="status" aria-label="会话运行中" title="会话运行中" alt="">`)
			} else if c != nil && c.unread {
				b.WriteString(`<span class="codex-history-unread" role="status" aria-label="会话已完成，尚未查看" title="会话已完成，尚未查看"></span>`)
			}
			b.WriteString(`<button class="codex-icon-button" type="button" data-ref="`)
			b.WriteString(html.EscapeString(renameRef))
			b.WriteString(`" title="修改会话标题" aria-label="修改会话标题" aria-haspopup="dialog"><img src="resources/icons/pencil.svg" alt=""></button></div></div>`)
			bindings = append(bindings, peb.EditorBinding{Ref: ref, Kind: "click"}, peb.EditorBinding{Ref: renameRef, Kind: "click"})
		}
		if a.history.NextCursor != "" {
			b.WriteString(`<button data-ref="more-history" type="button"`)
			if a.historyLoading {
				b.WriteString(` disabled`)
			}
			b.WriteString(`>更多会话</button>`)
			bindings = append(bindings, peb.EditorBinding{Ref: "more-history", Kind: "click"})
		}
	} else if a.historyLoading {
		b.WriteString(`<p class="codex-muted" role="status">正在加载历史会话…</p>`)
	} else {
		b.WriteString(`<p class="codex-muted">点击刷新，查看当前项目的历史会话。</p>`)
	}
	b.WriteString(`</div></section></aside></main>`)
	return &peb.EditorView{HTML: b.String(), CSS: chatCSS, Events: bindings}
}

func renderChatStatus(b *strings.Builder, status string) {
	if status != "" {
		b.WriteString(`<p class="codex-message codex-status" role="status">`)
		b.WriteString(html.EscapeString(status))
		b.WriteString(`</p>`)
	}
}

func (c *conversation) renderProgress(b *strings.Builder, bindings []peb.EditorBinding, progress *turnProgress, status string, collapsible bool) []peb.EditorBinding {
	progress.render(b, collapsible)
	if collapsible {
		bindings = append(bindings, peb.EditorBinding{Ref: progress.toggleRef, Kind: "click"})
	}
	if progress == c.progress {
		renderChatStatus(b, status)
	}
	return bindings
}

func (c *conversation) render(b *strings.Builder, bindings []peb.EditorBinding, status string) []peb.EditorBinding {
	b.WriteString(`<article class="codex-chat`)
	if c.busy {
		b.WriteString(` codex-chat-running`)
	}
	b.WriteString(`">`)
	if c.cursor != "" {
		b.WriteString(`<button data-ref="older" type="button">加载更早的记录</button>`)
		bindings = append(bindings, peb.EditorBinding{Ref: "older", Kind: "click"})
	}
	if len(c.items) == 0 && c.progress == nil {
		b.WriteString(`<div class="codex-empty"><h2>与 Codex 一起工作</h2><p>回复、命令执行和文件修改会显示在这里。</p></div>`)
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
			var fragment strings.Builder
			i.render(&fragment, c.context.ProjectPath, fmt.Sprintf("%p", i))
			i.html = fragment.String()
			if len(i.html) > 512<<10 {
				i.html = `<p class="codex-muted">此条输出过长，请在 Codex 历史中查看完整内容。</p>`
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
		b.WriteString(`<p class="codex-muted">较早输出已折叠，完整记录保存在 Codex 会话中。</p>`)
	}
	var progress *turnProgress
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
		progress = i.progress
		index = end
	}
	bindings = files.render(b, bindings)
	if c.progress != nil && c.progress != progress {
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
	b.WriteString(`</article>`)
	return bindings
}

func (c *conversation) renderTurn(b *strings.Builder, bindings []peb.EditorBinding, items []*chatItem, status string, latestActivity int) []peb.EditorBinding {
	p := items[0].progress
	if p == nil {
		return c.renderItems(b, bindings, items, latestActivity)
	}
	// Keep the prompt and final response outside the region replaced by a toggle.
	start := 0
	for start < len(items) && items[start].data.Type == "userMessage" {
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
	b.WriteString(`<section class="codex-turn"`)
	if p.id != "" {
		b.WriteString(` data-ref="turn/`)
		b.WriteString(html.EscapeString(p.id))
		b.WriteString(`/region" data-pencode-region`)
	}
	b.WriteString(`>`)
	bindings = c.renderProgress(b, bindings, p, status, collapsible)
	if !collapsible || p.expanded {
		bindings = c.renderItems(b, bindings, items[start:final], latestActivity-start)
		if final < len(items) {
			bindings = c.renderItems(b, bindings, items[final+1:], latestActivity-final-1)
		}
	}
	b.WriteString(`</section>`)
	if final < len(items) {
		bindings = c.renderItems(b, bindings, items[final:final+1], -1)
	}
	return bindings
}

func (c *conversation) renderItems(b *strings.Builder, bindings []peb.EditorBinding, items []*chatItem, latestActivity int) []peb.EditorBinding {
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
			b.WriteString(i.html)
			bindings = append(bindings, i.linkBindings...)
		}
		if i.detailRef != "" {
			bindings = append(bindings, peb.EditorBinding{Ref: i.detailRef, Kind: "click"})
		}
		index++
	}
	return bindings
}

func (i *chatItem) render(b *strings.Builder, root, scope string) {
	i.linkBindings = nil
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
	b.WriteString(`<section class="`)
	b.WriteString(class)
	b.WriteString(`" aria-label="`)
	b.WriteString(html.EscapeString(label))
	if i.data.ID != "" {
		b.WriteString(`" data-ref="message/`)
		b.WriteString(html.EscapeString(i.data.ID))
		b.WriteString(`" data-pencode-region="`)
	}
	b.WriteString(`">`)
	if i.data.Type == "userMessage" {
		i.renderImages(b)
		if i.text.Len() > 0 {
			b.WriteString(`<div class="codex-user-bubble">`)
		}
	}
	if activity {
		hasDetails := i.text.Len() > 0 || i.output.Len() > 0 || i.data.Command != "" || len(i.data.Changes) > 0
		if i.data.ID != "" && hasDetails {
			if i.detailRef == "" {
				i.detailRef = "item/" + i.data.ID
			}
			b.WriteString(`<button class="codex-activity-summary" type="button" data-ref="`)
			b.WriteString(html.EscapeString(i.detailRef))
			b.WriteString(`" aria-expanded="`)
			b.WriteString(strconv.FormatBool(i.expanded))
			b.WriteString(`">`)
		} else {
			i.detailRef = ""
			b.WriteString(`<div class="codex-activity-summary">`)
		}
		b.WriteString(`<img src="resources/icons/`)
		b.WriteString(icon)
		b.WriteString(`.svg" alt=""><span>`)
		if i.sweep {
			var text strings.Builder
			i.renderActivityLabel(&text, label)
			renderSweepText(b, html.UnescapeString(text.String()))
		} else {
			i.renderActivityLabel(b, label)
		}
		b.WriteString(`</span>`)
		renderActivityStatus(b, i.data.Status)
		if i.detailRef != "" {
			b.WriteString(`<img class="codex-activity-chevron" src="resources/icons/chevron-down.svg" alt=""></button>`)
		} else {
			b.WriteString(`</div>`)
		}
		if !hasDetails || i.detailRef != "" && !i.expanded {
			b.WriteString(`</section>`)
			return
		}
		b.WriteString(`<div class="codex-activity-details">`)
	}
	if i.text.Len() > 0 && (i.data.Type != "userMessage" || !renderQuestionReplies(b, i.text.String())) {
		if i.complete && (i.data.Type == "userMessage" || i.data.Type == "agentMessage" || i.data.Type == "plan") {
			i.linkBindings = append(i.linkBindings, renderMarkdown(b, i.text.String(), root, scope)...)
		} else {
			b.WriteString(`<div class="codex-text" data-pencode-preserve-whitespace>`)
			b.WriteString(html.EscapeString(i.text.String()))
			b.WriteString(`</div>`)
		}
	}
	if i.data.Command != "" {
		pre(b, i.data.Command)
	}
	if i.output.Len() > 0 {
		pre(b, i.output.String())
	}
	for _, change := range i.data.Changes {
		b.WriteString(`<h4>`)
		b.WriteString(html.EscapeString(change.Path))
		b.WriteString(`</h4>`)
		renderDiff(b, change.Diff[:min(len(change.Diff), 64<<10)], change.Kind.Type)
	}
	if activity {
		b.WriteString(`</div>`)
	}
	if i.data.Type == "userMessage" {
		if i.text.Len() > 0 {
			b.WriteString(`</div>`)
		}
		i.renderMessageActions(b)
	}
	b.WriteString(`</section>`)
}

func pre(b *strings.Builder, text string) {
	b.WriteString(`<pre><code>`)
	b.WriteString(html.EscapeString(text))
	b.WriteString(`</code></pre>`)
}

func renderMarkdown(b *strings.Builder, source, root, scope string) []peb.EditorBinding {
	document := chatParser.ParseStringSource(source)
	// Chat text must not initiate resource loads or leak the task in image URLs.
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if _, ok := node.(*ast.Image); entering && ok {
			node.Parent().RemoveChild(node)
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	links := chatLinkState{root: root, scope: scope}
	context := renderer.NewContext()
	context.Set(chatLinkContext, &links)
	if err := chatRenderer.RenderStringSource(b, source, document, renderer.WithContext(context)); err != nil {
		pre(b, source)
	}
	return links.bindings
}

func (r *approval) render(b *strings.Builder, bindings []peb.EditorBinding) []peb.EditorBinding {
	b.WriteString(`<section class="codex-approval"><h3>Codex 需要你的确认</h3>`)
	id := string(r.id)
	var formatted bytes.Buffer
	_ = json.Indent(&formatted, r.params, "", "  ")
	pre(b, formatted.String())
	bindings = actionButton(b, bindings, "accept/"+id, "允许本次操作")
	bindings = actionButton(b, bindings, "decline/"+id, "拒绝")
	b.WriteString(`</section>`)
	return bindings
}

func actionButton(b *strings.Builder, bindings []peb.EditorBinding, ref, label string) []peb.EditorBinding {
	b.WriteString(`<button type="button" data-ref="`)
	b.WriteString(html.EscapeString(ref))
	b.WriteString(`">`)
	b.WriteString(label)
	b.WriteString(`</button>`)
	return append(bindings, peb.EditorBinding{Ref: ref, Kind: "click"})
}
