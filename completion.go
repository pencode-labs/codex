package main

import (
	"html"
	"io/fs"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pencode-labs/pencode/peb"
)

const maxCompletions = 8

type composerCandidate struct {
	label, detail, path string
	score               int
	skill               *skillOption
}

type composerCompletion struct {
	kind                 byte
	start, end, selected int // Byte offsets in the draft; events use rune offsets.
	query                string
	items                [maxCompletions]composerCandidate
	count                int
	caret                int
	revision             uint64
	root                 string
	files                []completionFile
	indexed              time.Time
	error                string
	dismissed            string
	dismissedCaret       uint32
	skills               []skillOption
	skillsLoaded         bool
	skillError           string
}

type completionFile struct{ path, folded string }

func runeByteOffset(value string, offset uint32) int {
	for index := range value {
		if offset == 0 {
			return index
		}
		offset--
	}
	return len(value)
}

func completionToken(value string, anchor, focus uint32) (kind byte, start, end int) {
	if anchor != focus {
		return
	}
	end = runeByteOffset(value, focus)
	start = end
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(value[:start])
		if unicode.IsSpace(r) {
			break
		}
		start -= size
	}
	if start == end {
		return
	}
	if value[start] == '@' || value[start] == '/' && strings.TrimSpace(value[:start]) == "" {
		kind = value[start]
	}
	return
}

func (a *agent) draft() string {
	if a.selected != nil {
		return a.selected.draft
	}
	return a.drafts[a.context.ProjectPath]
}

func (a *agent) setDraft(value string) {
	if a.selected != nil {
		a.selected.draft = value
	} else {
		if a.drafts == nil {
			a.drafts = make(map[string]string)
		}
		a.drafts[a.context.ProjectPath] = value
	}
}

func (a *agent) dismissCompletion() {
	p := &a.completion
	if p.kind != 0 {
		p.kind, p.count, p.query = 0, 0, ""
		clear(p.items[:])
		a.workspaceDirty = true
	}
}

func (a *agent) updateCompletion(e peb.EditorEvent) {
	p := &a.completion
	if p.dismissed != "" && e.Value == p.dismissed && e.Anchor == p.dismissedCaret && e.Focus == p.dismissedCaret {
		return
	}
	p.dismissed = ""
	kind, start, end := completionToken(e.Value, e.Anchor, e.Focus)
	if kind == 0 {
		a.dismissCompletion()
		return
	}
	query := e.Value[start+1 : end]
	if p.kind == kind && p.start == start && p.end == end && p.query == query && (kind != '/' || p.skillsLoaded || p.skillError != "") {
		return
	}
	opening := p.kind != kind
	p.kind, p.start, p.end, p.query, p.selected, p.count = kind, start, end, query, 0, 0
	clear(p.items[:])
	if kind == '/' {
		if !p.skillsLoaded && (opening || p.skillError == "") {
			p.skillError = ""
			if err := a.loadSkills(); err != nil {
				p.skillError = "无法加载技能：" + err.Error()
			}
			if p.kind != kind {
				return
			}
		}
		folded := strings.ToLower(query)
		skills := p.skills
		if !p.skillsLoaded {
			skills = nil
		}
		for index := range skills {
			skill := &skills[index]
			if strings.Contains(skill.search, folded) {
				p.items[p.count] = composerCandidate{label: skill.Name, detail: skill.detail, skill: skill}
				p.count++
				if p.count == len(p.items) {
					break
				}
			}
		}
	} else {
		if p.root != a.context.ProjectPath || opening && time.Since(p.indexed) > 30*time.Second {
			p.indexFiles(a.context.ProjectPath)
		}
		folded := strings.ToLower(query)
		for _, file := range p.files {
			score := 0
			switch {
			case strings.HasPrefix(filepath.Base(file.folded), folded):
				score = 3
			case strings.Contains(filepath.Base(file.folded), folded):
				score = 2
			case strings.Contains(file.folded, folded):
				score = 1
			default:
				continue
			}
			position := 0
			for position < p.count && p.items[position].score >= score {
				position++
			}
			if position == len(p.items) {
				continue
			}
			p.count = min(p.count+1, len(p.items))
			copy(p.items[position+1:p.count], p.items[position:p.count-1])
			p.items[position] = composerCandidate{label: filepath.Base(file.path), detail: file.path, path: file.path, score: score}
		}
	}
	a.workspaceDirty = true
}

// Bound both traversal and retained paths. Never follow symlinks or scan on
// ordinary keystrokes; a project index is reused throughout a mention search.
func (p *composerCompletion) indexFiles(root string) {
	clear(p.files)
	p.root, p.files, p.indexed, p.error = root, p.files[:0], time.Now(), ""
	if !filepath.IsAbs(root) {
		p.error = "请先打开项目目录"
		return
	}
	visited := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		visited++
		if visited > 40000 || len(p.files) == 20000 {
			return fs.SkipAll
		}
		if err != nil {
			if path == root {
				return err
			}
			return nil
		}
		if entry.IsDir() {
			if path != root {
				switch entry.Name() {
				case ".git", ".hg", ".svn", "node_modules", "vendor", "target", ".venv", "__pycache__":
					return fs.SkipDir
				}
			}
			return nil
		}
		if entry.Type().IsRegular() {
			relative, err := filepath.Rel(root, path)
			if err == nil {
				relative = filepath.ToSlash(relative)
				p.files = append(p.files, completionFile{relative, strings.ToLower(relative)})
			}
		}
		return nil
	})
	if err != nil {
		p.error = "无法读取项目文件：" + err.Error()
	}
}

func (a *agent) editCompletion(value string, caret int) {
	a.setDraft(value)
	a.completion.caret = caret
	a.completion.revision++
	a.workspaceDirty = true
}

func (a *agent) handleCompletion(e peb.EditorEvent) error {
	p := &a.completion
	if p.kind == 0 {
		return nil
	}
	if e.Kind == "completion" {
		a.setDraft(e.Value)
	}
	if e.Data == "dismiss" {
		a.dismissCompletion()
		p.dismissed, p.dismissedCaret = a.draft(), e.Focus
		return nil
	}
	if e.Kind == "completion" {
		// Selection or text may have changed before this key was delivered.
		kind, start, end := completionToken(e.Value, e.Anchor, e.Focus)
		if kind != p.kind || start != p.start || end != p.end || e.Value[start+1:end] != p.query {
			a.updateCompletion(e)
			return nil
		}
	}
	switch e.Data {
	case "next", "previous":
		if p.count > 0 {
			delta := 1
			if e.Data == "previous" {
				delta = -1
			}
			p.selected = (p.selected + delta + p.count) % p.count
			a.workspaceDirty = true
		}
	case "accept":
		if p.count == 0 {
			return nil
		}
		item, kind := p.items[p.selected], p.kind
		draft := a.draft()
		if p.start < 0 || p.end > len(draft) || draft[p.start+1:p.end] != p.query {
			a.dismissCompletion()
			return nil
		}
		before, after := draft[:p.start], draft[p.end:]
		a.dismissCompletion()
		if kind == '@' {
			a.addAttachment(filepath.Join(a.context.ProjectPath, filepath.FromSlash(item.path)))
			label := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(filepath.Base(item.path), `\`, `\\`), `[`, `\[`), `]`, `\]`)
			path := strings.ReplaceAll(url.PathEscape(item.path), "%2F", "/")
			reference := "[@" + label + "](" + path + ") "
			a.editCompletion(before+reference+after, utf8.RuneCountInString(before)+utf8.RuneCountInString(reference))
			return nil
		}
		a.addSkill(item.skill)
		a.editCompletion(before+after, utf8.RuneCountInString(before))
	}
	return nil
}

func (a *agent) renderCompletion(b *strings.Builder, bindings []peb.EditorBinding) []peb.EditorBinding {
	p := &a.completion
	if p.kind == 0 {
		return bindings
	}
	b.WriteString(`<section class="codex-completion" aria-label="输入建议"><div class="codex-completion-heading"><span>`)
	if p.kind == '/' {
		b.WriteString("技能")
	} else {
		b.WriteString("项目文件")
	}
	b.WriteString(`</span><span>↑↓ 选择 · Enter / Tab 确认 · Esc 关闭</span></div><div id="pencode-extension-codex-completion-list" class="codex-completion-list" data-ref="completion-list" data-pencode-region role="listbox" aria-label="输入建议">`)
	for index, item := range p.items[:p.count] {
		ref := "completion/" + strconv.Itoa(index)
		b.WriteString(`<button id="pencode-extension-codex-completion-option-`)
		b.WriteString(strconv.Itoa(index))
		b.WriteString(`" type="button" class="codex-completion-item" role="option" data-ref="`)
		b.WriteString(ref)
		b.WriteString(`" aria-selected="`)
		b.WriteString(strconv.FormatBool(index == p.selected))
		b.WriteString(`">`)
		if p.kind == '/' {
			b.WriteString(`<img class="codex-completion-icon" src="resources/icons/layers-2.svg" alt="" aria-hidden="true">`)
		}
		b.WriteString(`<span class="codex-completion-label">`)
		b.WriteString(html.EscapeString(item.label))
		b.WriteString(`</span><span class="codex-completion-detail">`)
		b.WriteString(html.EscapeString(item.detail))
		b.WriteString(`</span></button>`)
		bindings = append(bindings, peb.EditorBinding{Ref: ref, Kind: "click"})
	}
	if p.count == 0 {
		message := "没有匹配的技能"
		if p.skillError != "" {
			message = p.skillError
		}
		if p.kind == '@' {
			message = "没有匹配的项目文件"
			if p.error != "" {
				message = p.error
			}
		}
		b.WriteString(`<p class="codex-muted" role="status">`)
		b.WriteString(html.EscapeString(message))
		b.WriteString(`</p>`)
	}
	b.WriteString(`</div></section>`)
	return bindings
}
