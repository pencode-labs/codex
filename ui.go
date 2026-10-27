package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
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
	for _, c := range a.conversations {
		if !c.open || !c.dirty {
			continue
		}
		view := c.view()
		if err := a.publisher.Show(peb.UI{ID: c.ID, Title: c.title(), Kind: peb.EditorTab, Context: c.context, View: view}); err != nil {
			fmt.Fprintln(stderrWriter, err)
		}
		c.dirty = false
	}
	if a.panelDirty && a.panelOpen {
		if err := a.publisher.Show(peb.UI{ID: "composer", Title: "Codex", Kind: peb.Panel, Context: a.context, View: a.composerView()}); err != nil {
			fmt.Fprintln(stderrWriter, err)
		}
		a.panelDirty = false
	}
}

func (a *agent) composerView() *peb.EditorView {
	var b strings.Builder
	p := a.permission()
	draft, title := a.drafts[a.context.ProjectPath], "新会话"
	busy := false
	if a.selected != nil {
		draft, title, busy = a.selected.draft, a.selected.title(), a.selected.busy
	}
	b.WriteString(`<section class="codex-composer"><form data-ref="form" class="codex-input-card" aria-label="`)
	b.WriteString(html.EscapeString(title))
	b.WriteString(`"><textarea data-ref="prompt" class="codex-prompt" aria-label="给 Codex 的任务" placeholder="和 Codex 一起工作" rows="2">`)
	b.WriteString(html.EscapeString(draft))
	b.WriteString(`</textarea><div class="codex-input-footer"><button data-ref="composer-menu" class="codex-add" type="button" title="新建与历史会话" aria-label="新建与历史会话" aria-expanded="`)
	b.WriteString(strconv.FormatBool(a.composerMenu))
	b.WriteString(`"><img src="resources/icons/plus.svg" alt=""></button><button data-ref="permission-settings" class="codex-access`)
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
		b.WriteString(`<button data-ref="stop" class="codex-send" type="button" title="停止任务" aria-label="停止任务"><img src="resources/icons/square.svg" alt=""></button>`)
	} else {
		b.WriteString(`<button data-ref="send" class="codex-send" type="button" title="发送" aria-label="发送"><img src="resources/icons/arrow-up.svg" alt=""></button>`)
	}
	b.WriteString(`</div></form>`)
	bindings := []peb.EditorBinding{{Ref: "prompt", Kind: "input"}, {Ref: "form", Kind: "submit"}, {Ref: "composer-menu", Kind: "click"}, {Ref: "permission-settings", Kind: "click"}, {Ref: "model-settings", Kind: "click"}}
	if a.composerMenu {
		b.WriteString(`<div class="codex-composer-options" role="group" aria-label="会话操作"><button data-ref="new" type="button">新会话</button><button data-ref="history" type="button">历史会话</button></div>`)
		bindings = append(bindings, peb.EditorBinding{Ref: "new", Kind: "click"}, peb.EditorBinding{Ref: "history", Kind: "click"})
	}
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
	if a.showHistory {
		b.WriteString(`<div class="codex-history"><h3>当前项目的会话</h3>`)
		if len(a.history.Data) == 0 {
			b.WriteString(`<p class="codex-muted">暂无历史会话。发送第一条消息开始工作。</p>`)
		}
		for _, t := range a.history.Data {
			ref := "history/" + t.ID
			b.WriteString(`<button class="codex-history-row" type="button" data-ref="`)
			b.WriteString(html.EscapeString(ref))
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
		b.WriteString(`</div>`)
	}
	b.WriteString(`</section>`)
	return &peb.EditorView{HTML: b.String(), CSS: chatCSS, Events: bindings}
}

func (c *conversation) view() *peb.EditorView {
	var b strings.Builder
	b.WriteString(`<article class="codex-chat"><div class="codex-toolbar"><strong>Codex</strong><span class="codex-muted">`)
	if c.busy {
		b.WriteString("正在工作…")
	} else {
		b.WriteString("会话记录")
	}
	b.WriteString(`</span><button data-ref="compose" type="button">继续对话</button>`)
	if c.busy {
		b.WriteString(`<button data-ref="stop" type="button">停止</button>`)
	}
	b.WriteString(`</div><p class="codex-muted">`)
	b.WriteString(html.EscapeString(c.Cwd))
	b.WriteString(`</p>`)
	bindings := []peb.EditorBinding{{Ref: "compose", Kind: "click"}}
	if c.busy {
		bindings = append(bindings, peb.EditorBinding{Ref: "stop", Kind: "click"})
	}
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
	for _, i := range c.items[start:] {
		b.WriteString(i.html)
	}
	for _, r := range c.requests {
		bindings = r.render(&b, bindings)
	}
	if c.status != "" {
		b.WriteString(`<p class="codex-status" role="status">`)
		b.WriteString(html.EscapeString(c.status))
		b.WriteString(`</p>`)
	}
	b.WriteString(`</article>`)
	return &peb.EditorView{HTML: b.String(), CSS: chatCSS, Events: bindings}
}

func (i *chatItem) render(b *strings.Builder) {
	label := ""
	class := "codex-message"
	switch i.data.Type {
	case "userMessage":
		label = "你"
		class += " codex-user"
	case "agentMessage":
		label = "Codex"
	case "plan":
		label = "计划"
	case "reasoning":
		label = "思考摘要"
	case "commandExecution":
		label = "命令 · " + i.data.Status
	case "fileChange":
		label = "文件修改 · " + i.data.Status
	case "mcpToolCall", "dynamicToolCall", "collabToolCall":
		label = "工具 · " + i.data.Server + " " + i.data.Tool + " · " + i.data.Status
	case "webSearch":
		label = "搜索 · " + i.data.Query
	case "contextCompaction":
		label = "上下文已压缩"
	default:
		return
	}
	b.WriteString(`<section class="`)
	b.WriteString(class)
	b.WriteString(`"><h3>`)
	b.WriteString(html.EscapeString(label))
	b.WriteString(`</h3>`)
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
