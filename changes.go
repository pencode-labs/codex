package main

import (
	"html"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pencode-labs/pencode/peb"
)

type changedFile struct {
	preview        *peb.UI
	path           string
	diffs          []fileDiff
	added, removed int
	expanded       bool
}

type fileDiff struct {
	text, kind string
}

type turnFiles struct {
	id, root, ref  string
	html           string
	liveHTML       string
	files          []changedFile
	liveBindings   []peb.EditorBinding
	bindings       []peb.EditorBinding
	added, removed int
	complete       bool
	collapsed      bool
}

func newTurnFiles(id, root string) *turnFiles {
	return &turnFiles{id: id, root: root, ref: "changes/" + id, collapsed: true}
}

// Count hunk lines, including content beginning with +++ or ---; those are
// headers only outside a hunk. Headerless fileChange diffs contain edit lines.
func diffLineCounts(diff string) (added, removed int) {
	hunk := false
	for line := range strings.Lines(diff) {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			hunk = false
		case strings.HasPrefix(line, "@@"):
			hunk = true
		case strings.HasPrefix(line, "+"):
			if hunk || !strings.HasPrefix(line, "+++ ") {
				added++
			}
		case strings.HasPrefix(line, "-"):
			if hunk || !strings.HasPrefix(line, "--- ") {
				removed++
			}
		}
	}
	return
}

func (c change) lineCounts() (added, removed int) {
	// Add/delete payloads carry file contents; updates carry a unified diff.
	if c.Kind.Type != "add" && c.Kind.Type != "delete" {
		return diffLineCounts(c.Diff)
	}
	lines := strings.Count(c.Diff, "\n")
	if c.Diff != "" && !strings.HasSuffix(c.Diff, "\n") {
		lines++
	}
	if c.Kind.Type == "add" {
		return lines, 0
	}
	return 0, lines
}

func (f *turnFiles) add(item threadItem) {
	if item.Type != "fileChange" || item.Status != "completed" {
		return
	}
	for _, edit := range item.Changes {
		if edit.Path == "" {
			continue
		}
		path := filepath.Clean(edit.Path)
		if filepath.IsAbs(path) && f.root != "" {
			if rel, err := filepath.Rel(f.root, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				path = rel
			}
		}
		index := 0
		for index < len(f.files) && f.files[index].path != path {
			index++
		}
		if index == len(f.files) {
			f.files = append(f.files, changedFile{path: path})
		}
		file := &f.files[index]
		file.preview = nil
		added, removed := edit.lineCounts()
		file.added += added
		file.removed += removed
		file.diffs = append(file.diffs, fileDiff{text: edit.Diff, kind: edit.Kind.Type})
		f.added += added
		f.removed += removed
	}
	f.html = ""
	f.liveHTML = ""
}

func (f *turnFiles) prepareLive() {
	if f == nil || f.complete || len(f.files) == 0 || f.liveHTML != "" {
		return
	}
	var b strings.Builder
	f.liveBindings = f.liveBindings[:0]
	b.WriteString(`<div class="codex-live-changes"><div class="codex-live-changes-anchor"><button type="button" data-ref="live-changes" class="codex-live-changes-pill" aria-describedby="pencode-extension-codex-live-changes-list">`)
	writeActivityCount(&b, len(f.files))
	b.WriteString(` 个文件已更改`)
	renderChangeCounts(&b, f.added, f.removed)
	b.WriteString(`</button><div class="codex-live-changes-popover"><div id="pencode-extension-codex-live-changes-list" class="codex-live-changes-list" role="list" aria-label="本轮已更改的文件">`)
	for index, file := range f.files {
		ref := "change-preview/" + f.id + "/" + strconv.Itoa(index)
		b.WriteString(`<div role="listitem"><button type="button" class="codex-live-change" data-ref="`)
		b.WriteString(html.EscapeString(ref))
		b.WriteString(`"><span class="codex-change-path" title="`)
		b.WriteString(html.EscapeString(file.path))
		b.WriteString(`">`)
		b.WriteString(html.EscapeString(filepath.Base(file.path)))
		b.WriteString(`</span>`)
		renderChangeCounts(&b, file.added, file.removed)
		b.WriteString(`</button></div>`)
		f.liveBindings = append(f.liveBindings, peb.EditorBinding{Ref: ref, Kind: "click"})
	}
	b.WriteString(`</div></div></div></div>`)
	f.liveHTML = b.String()
}

func (a *agent) openChangePreview(ref string) error {
	separator := strings.LastIndexByte(ref, '/')
	if separator < 0 || a.selected == nil {
		return nil
	}
	index, err := strconv.Atoi(ref[separator+1:])
	f := a.selected.turnFiles(ref[:separator])
	if err != nil || f == nil || index < 0 || index >= len(f.files) {
		return nil
	}
	file := &f.files[index]
	if file.preview == nil {
		var b strings.Builder
		b.WriteString(`<section class="codex-change-preview" data-ref="change-preview" data-pencode-selectable data-pencode-searchable><header><h2>`)
		b.WriteString(html.EscapeString(filepath.Base(file.path)))
		b.WriteString(`</h2>`)
		renderChangeCounts(&b, file.added, file.removed)
		b.WriteString(`</header><p class="codex-muted">`)
		b.WriteString(html.EscapeString(file.path))
		b.WriteString(`</p>`)
		budget := 32 << 10
		renderFileDiffs(&b, *file, &budget)
		b.WriteString(`</section>`)
		file.preview = &peb.UI{
			ID: previewID("change", f.root+"/"+a.selected.ID+"/"+ref), Title: filepath.Base(file.path) + " · 差异预览",
			Kind: peb.EditorTab, Context: a.selected.context,
			View: &peb.EditorView{HTML: b.String(), CSS: chatCSS},
		}
	}
	if err := a.publisher.Show(*file.preview); err != nil {
		return err
	}
	// A view update preserves selection; explicitly reveal a reused preview tab.
	ui := *file.preview
	ui.View = nil
	return a.publisher.Show(ui)
}

func (f *turnFiles) prepare() {
	if f == nil || !f.complete || len(f.files) == 0 || f.html != "" {
		return
	}
	var b strings.Builder
	f.bindings = f.bindings[:0]
	b.WriteString(`<section class="codex-changes" aria-label="本轮文件变动" data-ref="`)
	b.WriteString(html.EscapeString(f.ref))
	b.WriteString(`/region" data-pencode-region><div class="codex-changes-header"><img src="resources/icons/pencil.svg" alt=""><div class="codex-changes-title"><span>已编辑 `)
	writeActivityCount(&b, len(f.files))
	b.WriteString(` 个文件</span>`)
	renderChangeCounts(&b, f.added, f.removed)
	b.WriteString(`</div><button type="button" data-ref="`)
	viewRef := "changes-diff/" + f.id
	b.WriteString(html.EscapeString(viewRef))
	b.WriteString(`" aria-expanded="`)
	allExpanded := f.allExpanded()
	b.WriteString(strconv.FormatBool(allExpanded))
	b.WriteString(`">`)
	if allExpanded {
		b.WriteString("收起变更")
	} else {
		b.WriteString("查看变更")
	}
	b.WriteString(`</button></div>`)
	f.bindings = append(f.bindings, peb.EditorBinding{Ref: viewRef, Kind: "click"})
	b.WriteString(`<div class="codex-changes-list`)
	if f.collapsed {
		b.WriteString(` codex-changes-preview`)
	}
	b.WriteString(`" data-ref="`)
	b.WriteString(html.EscapeString(f.ref))
	b.WriteString(`/list"`)
	if !f.collapsed {
		b.WriteString(` data-pencode-scroll="preserve"`)
	}
	b.WriteString(`>`)
	// Keep expanded diffs within the transcript's bounded display budget.
	diffBudget := 32 << 10
	files := f.files
	if f.collapsed {
		files = files[:min(3, len(files))]
	}
	for index, file := range files {
		if b.Len() > 256<<10 {
			b.WriteString(`<p class="codex-muted">更多文件已省略，完整记录保存在 Codex 会话中。</p>`)
			break
		}
		ref := "change-file/" + f.id + "/" + strconv.Itoa(index)
		b.WriteString(`<button class="codex-change-file" type="button" data-ref="`)
		b.WriteString(html.EscapeString(ref))
		expanded := file.expanded
		b.WriteString(`" aria-expanded="`)
		b.WriteString(strconv.FormatBool(expanded))
		b.WriteString(`" title="`)
		b.WriteString(html.EscapeString(file.path))
		b.WriteString(`"><span class="codex-change-path">`)
		b.WriteString(html.EscapeString(filepath.Base(file.path)))
		b.WriteString(`</span>`)
		renderChangeCounts(&b, file.added, file.removed)
		b.WriteString(`</button>`)
		f.bindings = append(f.bindings, peb.EditorBinding{Ref: ref, Kind: "click"})
		if expanded {
			renderFileDiffs(&b, file, &diffBudget)
		}
	}
	b.WriteString(`</div>`)
	b.WriteString(`<button class="codex-changes-toggle" type="button" data-ref="`)
	b.WriteString(html.EscapeString(f.ref))
	b.WriteString(`" aria-expanded="`)
	b.WriteString(strconv.FormatBool(!f.collapsed))
	b.WriteString(`">`)
	if f.collapsed {
		b.WriteString("展开文件")
	} else {
		b.WriteString("收起文件")
	}
	b.WriteString(`<img class="codex-activity-chevron" src="resources/icons/chevron-down.svg" alt=""></button></section>`)
	f.bindings = append(f.bindings, peb.EditorBinding{Ref: f.ref, Kind: "click"})
	f.html = b.String()
}

func renderFileDiffs(b *strings.Builder, file changedFile, budget *int) {
	b.WriteString(`<div class="codex-change-diff">`)
	for _, diff := range file.diffs {
		if *budget == 0 {
			break
		}
		text := diff.text[:min(len(diff.text), min(*budget, 16<<10))]
		text = strings.ToValidUTF8(text, "")
		renderDiff(b, text, diff.kind)
		*budget -= len(text)
		if len(text) < len(diff.text) {
			b.WriteString(`<p class="codex-muted">部分 diff 已省略，完整记录保存在 Codex 会话中。</p>`)
		}
	}
	if *budget == 0 {
		b.WriteString(`<p class="codex-muted">已达到 diff 显示上限，完整记录保存在 Codex 会话中。</p>`)
	}
	b.WriteString(`</div>`)
}

func renderDiff(b *strings.Builder, text, kind string) {
	b.WriteString(`<pre><code class="codex-diff">`)
	class, start, end, hunk := "", 0, 0, false
	for line := range strings.Lines(text) {
		next := ""
		switch {
		case kind == "add":
			next = "added"
		case kind == "delete":
			next = "removed"
		case strings.HasPrefix(line, "diff --git "):
			next, hunk = "header", false
		case strings.HasPrefix(line, "@@"):
			next, hunk = "hunk", true
		case !hunk && (strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ")):
			next = "header"
		case strings.HasPrefix(line, "+"):
			next = "added"
		case strings.HasPrefix(line, "-"):
			next = "removed"
		}
		if next != class {
			b.WriteString(html.EscapeString(text[start:end]))
			if class != "" {
				b.WriteString(`</span>`)
			}
			// Bound markup as well as source text for diffs with many short edits.
			if b.Len() > 224<<10 {
				b.WriteString(`</code></pre><p class="codex-muted">部分 diff 已省略，完整记录保存在 Codex 会话中。</p>`)
				return
			}
			if next != "" {
				b.WriteString(`<span class="codex-diff-`)
				b.WriteString(next)
				b.WriteString(`">`)
			}
			class, start = next, end
		}
		end += len(line)
	}
	b.WriteString(html.EscapeString(text[start:end]))
	if class != "" {
		b.WriteString(`</span>`)
	}
	b.WriteString(`</code></pre>`)
}

func renderChangeCounts(b *strings.Builder, added, removed int) {
	b.WriteString(`<span class="codex-change-counts"><span class="codex-change-added">+`)
	writeActivityCount(b, added)
	b.WriteString(`</span><span class="codex-change-removed">-`)
	writeActivityCount(b, removed)
	b.WriteString(`</span></span>`)
}

func (f *turnFiles) render(b *strings.Builder, bindings []peb.EditorBinding) []peb.EditorBinding {
	if f == nil {
		return bindings
	}
	f.prepare()
	b.WriteString(f.html)
	return append(bindings, f.bindings...)
}

func (f *turnFiles) allExpanded() bool {
	if f.collapsed {
		return false
	}
	for _, file := range f.files {
		if !file.expanded {
			return false
		}
	}
	return true
}

func (c *conversation) turnFiles(id string) *turnFiles {
	if c.files != nil && c.files.id == id {
		return c.files
	}
	for _, item := range c.items {
		if item.files != nil && item.files.id == id {
			return item.files
		}
	}
	return nil
}

func (c *conversation) handleChangesClick(ref string) bool {
	kind, rest, ok := strings.Cut(ref, "/")
	if !ok || kind != "changes" && kind != "changes-diff" && kind != "change-file" {
		return false
	}
	index := -1
	if kind == "change-file" {
		separator := strings.LastIndexByte(rest, '/')
		if separator < 0 {
			return true
		}
		var err error
		index, err = strconv.Atoi(rest[separator+1:])
		if err != nil || index < 0 {
			return true
		}
		rest = rest[:separator]
	}
	f := c.turnFiles(rest)
	if f == nil || len(f.files) == 0 || kind != "change-file" && !f.complete {
		return true
	}
	switch kind {
	case "change-file":
		if index >= len(f.files) {
			return true
		}
		f.files[index].expanded = !f.files[index].expanded
	case "changes":
		f.collapsed = !f.collapsed
		if f.collapsed {
			for index := range f.files {
				f.files[index].expanded = false
			}
		}
	case "changes-diff":
		expand := !f.allExpanded()
		f.collapsed = false
		for index := range f.files {
			f.files[index].expanded = expand
		}
	}
	f.html, f.liveHTML = "", ""
	c.dirty = true
	return true
}
