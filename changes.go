package main

import (
	"html"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pencode-labs/pencode/peb"
)

type changedFile struct {
	path           string
	added, removed int
	diffs          []string
	expanded       bool
}

type turnFiles struct {
	id, root, ref  string
	files          []changedFile
	added, removed int
	complete       bool
	collapsed      bool
	html           string
	bindings       []peb.EditorBinding
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
		added, removed := edit.lineCounts()
		file.added += added
		file.removed += removed
		file.diffs = append(file.diffs, edit.Diff)
		f.added += added
		f.removed += removed
	}
	f.html = ""
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
		b.WriteString(`" aria-expanded="`)
		expanded := file.expanded && !f.collapsed
		b.WriteString(strconv.FormatBool(expanded))
		b.WriteString(`" title="`)
		b.WriteString(html.EscapeString(file.path))
		b.WriteString(`"><span class="codex-change-path">`)
		b.WriteString(html.EscapeString(file.path))
		b.WriteString(`</span>`)
		renderChangeCounts(&b, file.added, file.removed)
		b.WriteString(`</button>`)
		f.bindings = append(f.bindings, peb.EditorBinding{Ref: ref, Kind: "click"})
		if expanded {
			b.WriteString(`<div class="codex-change-diff">`)
			for _, diff := range file.diffs {
				if diffBudget == 0 {
					break
				}
				text := diff[:min(len(diff), min(diffBudget, 16<<10))]
				text = strings.ToValidUTF8(text, "")
				pre(&b, text)
				diffBudget -= len(text)
				if len(text) < len(diff) {
					b.WriteString(`<p class="codex-muted">部分 diff 已省略，完整记录保存在 Codex 会话中。</p>`)
				}
			}
			if diffBudget == 0 {
				b.WriteString(`<p class="codex-muted">已达到 diff 显示上限，完整记录保存在 Codex 会话中。</p>`)
			}
			b.WriteString(`</div>`)
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
	id, index := rest, ""
	if kind == "change-file" {
		id, index, _ = strings.Cut(rest, "/")
	}
	f := c.turnFiles(id)
	if f == nil || !f.complete || len(f.files) == 0 {
		return true
	}
	switch kind {
	case "changes":
		f.collapsed = !f.collapsed
	case "changes-diff":
		expand := !f.allExpanded()
		f.collapsed = false
		for index := range f.files {
			f.files[index].expanded = expand
		}
	case "change-file":
		n, err := strconv.Atoi(index)
		if err != nil || n < 0 || n >= len(f.files) {
			return true
		}
		f.files[n].expanded = f.collapsed || !f.files[n].expanded
		f.collapsed = false
	}
	f.html = ""
	c.dirty = true
	return true
}
