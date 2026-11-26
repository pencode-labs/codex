package main

import (
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/onix-engine/onix/html/markup"
	"github.com/pencode-labs/pencode/peb"
)

type changedFile struct {
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
	b := markup.Acquire()
	defer b.Release()
	b.Open("div", "class", "codex-live-changes")
	b.Open("div", "class", "codex-live-changes-anchor")
	b.Open("button", "type", "button", "data-ref", "live-changes", "class", "codex-live-changes-pill", "aria-describedby", "pencode-extension-codex-live-changes-list")
	b.Text(strconv.Itoa(len(f.files)))
	b.Text(" 个文件已更改")
	renderChangeCounts(b, f.added, f.removed)
	b.End()
	b.Open("div", "class", "codex-live-changes-popover")
	b.Open("div", "id", "pencode-extension-codex-live-changes-list", "class", "codex-live-changes-list", "role", "list", "aria-label", "本轮已更改的文件")
	for index, file := range f.files {
		ref := "change-preview/" + f.id + "/" + strconv.Itoa(index)
		name := file.path
		if !filepath.IsAbs(name) {
			name = filepath.Join(f.root, name)
		}
		target := url.URL{Scheme: "pencode-diff", Path: "/" + strings.TrimPrefix(filepath.ToSlash(name), "/")}
		b.Open("div", "role", "listitem")
		b.Open("button", "type", "button", "role", "link", "class", "codex-live-change", "data-ref", ref, "data-pencode-href", target.String())
		b.TextElement("span", filepath.Base(file.path), "class", "codex-change-path", "title", file.path)
		renderChangeCounts(b, file.added, file.removed)
		b.End()
		b.End()
	}
	b.End()
	b.End()
	b.End()
	b.End()
	f.liveHTML = b.String()
}

func (f *turnFiles) prepare() {
	if f == nil || !f.complete || len(f.files) == 0 || f.html != "" {
		return
	}
	b := markup.Acquire()
	defer b.Release()
	f.bindings = f.bindings[:0]
	b.Open("section", "class", "codex-changes", "aria-label", "本轮文件变动", "data-ref", f.ref+"/region", "data-pencode-region", "")
	b.Open("div", "class", "codex-changes-header")
	b.Element("img", "src", "resources/icons/pencil.svg", "alt", "")
	b.Open("div", "class", "codex-changes-title")
	b.Open("span")
	b.Text("已编辑 ")
	b.Text(strconv.Itoa(len(f.files)))
	b.Text(" 个文件")
	b.End()
	renderChangeCounts(b, f.added, f.removed)
	b.End()
	viewRef := "changes-diff/" + f.id
	allExpanded := f.allExpanded()
	b.Open("button", "type", "button", "data-ref", viewRef, "aria-expanded", strconv.FormatBool(allExpanded))
	if allExpanded {
		b.Text("收起变更")
	} else {
		b.Text("查看变更")
	}
	b.End()
	b.End()
	f.bindings = append(f.bindings, peb.EditorBinding{Ref: viewRef, Kind: "click"})
	class := "codex-changes-list"
	if f.collapsed {
		class += " codex-changes-preview"
	}
	list := b.Open("div", "class", class, "data-ref", f.ref+"/list")
	if !f.collapsed {
		list.SetAttr("data-pencode-scroll", "preserve")
	}
	// Keep expanded diffs within the transcript's bounded display budget.
	diffBudget := 32 << 10
	files := f.files
	if f.collapsed {
		files = files[:min(3, len(files))]
	}
	for index, file := range files {
		if b.SizeHint() > 256<<10 {
			b.TextElement("p", "更多文件已省略，完整记录保存在 Codex 会话中。", "class", "codex-muted")
			break
		}
		ref := "change-file/" + f.id + "/" + strconv.Itoa(index)
		expanded := file.expanded
		b.Open("button", "class", "codex-change-file", "type", "button", "data-ref", ref, "aria-expanded", strconv.FormatBool(expanded), "title", file.path)
		b.TextElement("span", filepath.Base(file.path), "class", "codex-change-path")
		renderChangeCounts(b, file.added, file.removed)
		b.End()
		f.bindings = append(f.bindings, peb.EditorBinding{Ref: ref, Kind: "click"})
		if expanded {
			renderFileDiffs(b, file, &diffBudget)
		}
	}
	b.End()
	b.Open("button", "class", "codex-changes-toggle", "type", "button", "data-ref", f.ref, "aria-expanded", strconv.FormatBool(!f.collapsed))
	if f.collapsed {
		b.Text("展开文件")
	} else {
		b.Text("收起文件")
	}
	b.Element("img", "class", "codex-activity-chevron", "src", "resources/icons/chevron-down.svg", "alt", "")
	b.End()
	b.End()
	f.bindings = append(f.bindings, peb.EditorBinding{Ref: f.ref, Kind: "click"})
	f.html = b.String()
}

func renderFileDiffs(b *markup.Builder, file changedFile, budget *int) {
	b.Open("div", "class", "codex-change-diff")
	for _, diff := range file.diffs {
		if *budget == 0 {
			break
		}
		text := diff.text[:min(len(diff.text), min(*budget, 16<<10))]
		text = strings.ToValidUTF8(text, "")
		renderDiff(b, text, diff.kind)
		*budget -= len(text)
		if len(text) < len(diff.text) {
			b.TextElement("p", "部分 diff 已省略，完整记录保存在 Codex 会话中。", "class", "codex-muted")
		}
	}
	if *budget == 0 {
		b.TextElement("p", "已达到 diff 显示上限，完整记录保存在 Codex 会话中。", "class", "codex-muted")
	}
	b.End()
}

func renderDiff(b *markup.Builder, text, kind string) {
	b.Open("pre")
	b.Open("code", "class", "codex-diff")
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
			b.Text(text[start:end])
			if class != "" {
				b.End()
			}
			// Bound markup as well as source text for diffs with many short edits.
			if b.SizeHint() > 224<<10 {
				b.End()
				b.End()
				b.TextElement("p", "部分 diff 已省略，完整记录保存在 Codex 会话中。", "class", "codex-muted")
				return
			}
			if next != "" {
				b.Open("span", "class", "codex-diff-"+next)
			}
			class, start = next, end
		}
		end += len(line)
	}
	b.Text(text[start:end])
	if class != "" {
		b.End()
	}
	b.End()
	b.End()
}

func renderChangeCounts(b *markup.Builder, added, removed int) {
	b.Open("span", "class", "codex-change-counts")
	b.Open("span", "class", "codex-change-added")
	b.Text("+")
	b.Text(strconv.Itoa(added))
	b.End()
	b.Open("span", "class", "codex-change-removed")
	b.Text("-")
	b.Text(strconv.Itoa(removed))
	b.End()
	b.End()
}

func (f *turnFiles) render(b *markup.Builder, bindings []peb.EditorBinding) []peb.EditorBinding {
	if f == nil {
		return bindings
	}
	f.prepare()
	b.Embed(f.html)
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
