package main

import (
	"bytes"
	_ "embed"
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
	markdownhtml "github.com/yuin/goldmark/v2/renderer/html"
)

//go:embed resources/chat.css
var chatCSS string

var chatParser = parser.New(parser.WithExtensions(extension.TableParser, extension.NewLinkifyParser()))
var chatRenderer = markdownhtml.New(markdownhtml.WithExtensions(extension.TableHTMLRenderer))

func (a *agent) flush() {
	if !a.dirty() {
		return
	}
	title := "Codex 新会话"
	if a.selected != nil {
		title = a.selected.title()
	}
	var view *peb.EditorView
	if a.composerPrefix == "" || a.workspaceDirty || a.selected != nil && a.selected.dirty {
		view = a.composerView()
	} else {
		view = a.historyView()
	}
	if err := a.publisher.Show(peb.UI{ID: "composer", Title: title, Kind: peb.EditorTab, Context: a.context, View: view}); err != nil {
		fmt.Fprintln(stderrWriter, err)
		return
	}
	if a.revealWorkspace {
		if err := a.publisher.Show(peb.UI{ID: "composer", Title: title, Kind: peb.EditorTab, Context: a.context}); err != nil {
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
	b.WriteString(`<main class="codex-workspace" data-pencode-layout="fill"><section class="codex-chat-pane"><div class="codex-transcript" data-ref="transcript" data-pencode-scroll="end" data-pencode-scroll-key="`)
	if a.selected != nil {
		b.WriteString(html.EscapeString(a.selected.ID))
	}
	b.WriteString(`">`)
	if a.selected != nil {
		bindings = a.selected.render(&b, bindings)
	} else {
		b.WriteString(`<article class="codex-chat"><div class="codex-empty"><h2>与 Codex 一起工作</h2><p>回复、命令执行和文件修改会显示在这里。</p></div></article>`)
	}
	b.WriteString(`</div>`)
	p := a.permission()
	draft, title := a.drafts[a.context.ProjectPath], "新会话"
	busy := false
	if a.selected != nil {
		draft, title, busy = a.selected.draft, a.selected.title(), a.selected.busy
	}
	b.WriteString(`<section class="codex-composer"><form data-ref="form" data-pencode-focus="prompt" class="codex-input-card" aria-label="`)
	b.WriteString(html.EscapeString(title))
	b.WriteString(`">`)
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
	b.WriteString(`<textarea data-ref="prompt" data-pencode-autosize data-pencode-submit-on-enter="form" class="codex-prompt" aria-label="给 Codex 的任务" placeholder="和 Codex 一起工作" rows="2">`)
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
	if busy {
		b.WriteString(`<button data-ref="stop" class="codex-icon-button codex-send" type="button" title="停止任务" aria-label="停止任务"><img src="resources/icons/square.svg" alt=""></button>`)
	} else {
		b.WriteString(`<button data-ref="send" class="codex-icon-button codex-send" type="button" title="发送" aria-label="发送"><img src="resources/icons/arrow-up.svg" alt=""></button>`)
	}
	b.WriteString(`</div></form>`)
	bindings = append(bindings, peb.EditorBinding{Ref: "prompt", Kind: "input"}, peb.EditorBinding{Ref: "form", Kind: "submit"}, peb.EditorBinding{Ref: "add-file", Kind: "click"}, peb.EditorBinding{Ref: "permission-settings", Kind: "click"}, peb.EditorBinding{Ref: "model-settings", Kind: "click"})
	status := a.status
	if a.selected != nil && a.selected.status != "" {
		status = a.selected.status
	}
	if status != "" {
		b.WriteString(`<p class="codex-status" role="status">`)
		b.WriteString(html.EscapeString(status))
		b.WriteString(`</p>`)
	}
	if busy {
		bindings = append(bindings, peb.EditorBinding{Ref: "stop", Kind: "click"})
	} else {
		bindings = append(bindings, peb.EditorBinding{Ref: "send", Kind: "click"})
	}
	b.WriteString(`</section></section><aside class="codex-sidebar" aria-label="历史会话"><section class="codex-history"><div class="codex-history-header"><h3>当前项目的会话</h3><button data-ref="new" class="codex-icon-button" type="button" title="新会话" aria-label="新会话"><img data-ref="new-icon" data-pencode-icon="edit" alt=""></button><button data-ref="refresh-history" class="codex-icon-button" type="button" title="刷新会话" aria-label="刷新会话"><img data-ref="refresh-history-icon" data-pencode-icon="refresh-cw" alt=""></button></div><div class="codex-history-list" data-ref="history-list" data-pencode-region data-pencode-scroll="preserve" data-pencode-scroll-key="`)
	b.WriteString(html.EscapeString(a.context.ProjectPath))
	b.WriteString(`">`)
	bindings = append(bindings, peb.EditorBinding{Ref: "new", Kind: "click"}, peb.EditorBinding{Ref: "refresh-history", Kind: "click"})
	a.composerPrefix = b.String()
	a.composerBindings = bindings
	return a.historyView()
}

func (a *agent) historyView() *peb.EditorView {
	var b strings.Builder
	b.WriteString(a.composerPrefix)
	bindings := make([]peb.EditorBinding, len(a.composerBindings), len(a.composerBindings)+len(a.history.Data)+1)
	copy(bindings, a.composerBindings)
	if a.showHistory {
		if len(a.history.Data) == 0 {
			b.WriteString(`<p class="codex-muted">暂无历史会话。发送第一条消息开始工作。</p>`)
		}
		for _, t := range a.history.Data {
			ref := "history/" + t.ID
			b.WriteString(`<button class="codex-history-row" type="button" data-ref="`)
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
			bindings = append(bindings, peb.EditorBinding{Ref: ref, Kind: "click"})
		}
		if a.history.NextCursor != "" {
			b.WriteString(`<button data-ref="more-history" type="button">更多会话</button>`)
			bindings = append(bindings, peb.EditorBinding{Ref: "more-history", Kind: "click"})
		}
	} else {
		b.WriteString(`<p class="codex-muted">点击刷新，查看当前项目的历史会话。</p>`)
	}
	b.WriteString(`</div></section></aside></main>`)
	return &peb.EditorView{HTML: b.String(), CSS: chatCSS, Events: bindings}
}

func (c *conversation) render(b *strings.Builder, bindings []peb.EditorBinding) []peb.EditorBinding {
	b.WriteString(`<article class="codex-chat">`)
	if c.cursor != "" {
		b.WriteString(`<button data-ref="older" type="button">加载更早的记录</button>`)
		bindings = append(bindings, peb.EditorBinding{Ref: "older", Kind: "click"})
	}
	if len(c.items) == 0 {
		b.WriteString(`<div class="codex-empty"><h2>与 Codex 一起工作</h2><p>回复、命令执行和文件修改会显示在这里。</p></div>`)
	}
	// Render only changed items. Reserve room for approvals and keep the latest
	// messages visible even when an old command produced a large amount of output.
	start, size := len(c.items), 0
	for start > 0 {
		i := c.items[start-1]
		if i.html == "" {
			var fragment strings.Builder
			i.render(&fragment)
			i.html = fragment.String()
			if len(i.html) > 512<<10 {
				i.html = `<p class="codex-muted">此条输出过长，请在 Codex 历史中查看完整内容。</p>`
				i.detailRef = ""
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
	for index := start; index < len(c.items); {
		i := c.items[index]
		if i.activityKinds() != 0 {
			end := index + 1
			for end < len(c.items) && c.items[end].activityKinds() != 0 {
				end++
			}
			bindings = renderActivityGroup(b, bindings, c.items[index:end])
			index = end
			continue
		}
		b.WriteString(i.html)
		if i.detailRef != "" {
			bindings = append(bindings, peb.EditorBinding{Ref: i.detailRef, Kind: "click"})
		}
		index++
	}
	for _, r := range c.requests {
		bindings = r.render(b, bindings)
	}
	b.WriteString(`</article>`)
	return bindings
}

func (i *chatItem) render(b *strings.Builder) {
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
		label = "思考摘要"
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
	b.WriteString(`">`)
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
		i.renderActivityLabel(b, label)
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
	if i.text.Len() > 0 {
		if i.complete && (i.data.Type == "agentMessage" || i.data.Type == "plan") {
			renderMarkdown(b, i.text.String())
		} else {
			b.WriteString(`<div class="codex-text">`)
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
		pre(b, change.Diff[:min(len(change.Diff), 64<<10)])
	}
	if activity {
		b.WriteString(`</div>`)
	}
	b.WriteString(`</section>`)
}

func pre(b *strings.Builder, text string) {
	b.WriteString(`<pre><code>`)
	b.WriteString(html.EscapeString(text))
	b.WriteString(`</code></pre>`)
}

func renderMarkdown(b *strings.Builder, source string) {
	document := chatParser.ParseStringSource(source)
	// Chat text must not initiate resource loads or leak the task in image URLs.
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if _, ok := node.(*ast.Image); entering && ok {
			node.Parent().RemoveChild(node)
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	if err := chatRenderer.RenderStringSource(b, source, document); err != nil {
		pre(b, source)
	}
}

func (r *approval) render(b *strings.Builder, bindings []peb.EditorBinding) []peb.EditorBinding {
	b.WriteString(`<section class="codex-approval"><h3>Codex 需要你的确认</h3>`)
	id := string(r.id)
	if r.method == "item/tool/requestUserInput" {
		var params struct {
			Questions []struct {
				ID       string `json:"id"`
				Question string `json:"question"`
				IsSecret bool   `json:"isSecret"`
				Options  []struct {
					Label       string `json:"label"`
					Description string `json:"description"`
				} `json:"options"`
			} `json:"questions"`
		}
		if json.Unmarshal(r.params, &params) == nil {
			for _, q := range params.Questions {
				b.WriteString(`<p>`)
				b.WriteString(html.EscapeString(q.Question))
				b.WriteString(`</p>`)
				for _, option := range q.Options {
					b.WriteString(`<p class="codex-muted">`)
					b.WriteString(html.EscapeString(option.Label + " — " + option.Description))
					b.WriteString(`</p>`)
				}
				ref := "answer/" + id + "/" + q.ID
				b.WriteString(`<input data-ref="`)
				b.WriteString(html.EscapeString(ref))
				b.WriteString(`" aria-label="`)
				b.WriteString(html.EscapeString(q.Question))
				b.WriteString(`" value="`)
				b.WriteString(html.EscapeString(r.answers[q.ID]))
				b.WriteString(`"`)
				if q.IsSecret {
					b.WriteString(` type="password"`)
				}
				b.WriteString(`>`)
				bindings = append(bindings, peb.EditorBinding{Ref: ref, Kind: "input"})
			}
		}
		bindings = actionButton(b, bindings, "answer/"+id, "提交回答")
	} else {
		var formatted bytes.Buffer
		_ = json.Indent(&formatted, r.params, "", "  ")
		pre(b, formatted.String())
		bindings = actionButton(b, bindings, "accept/"+id, "允许本次操作")
		bindings = actionButton(b, bindings, "decline/"+id, "拒绝")
	}
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
