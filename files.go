package main

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/pencode-labs/pencode/peb"
)

type attachmentScope struct{ project, thread string }

type filePicker struct {
	directory, path, error string
	entries                []os.DirEntry
}

func (a *agent) attachmentScope() attachmentScope {
	key := attachmentScope{project: a.context.ProjectPath}
	if a.selected != nil {
		key.thread = a.selected.ID
	}
	return key
}

func (a *agent) addAttachment(path string) {
	key := a.attachmentScope()
	if slices.Contains(a.attachments[key], path) {
		return
	}
	if a.attachments == nil {
		a.attachments = make(map[attachmentScope][]string)
	}
	a.attachments[key] = append(a.attachments[key], path)
	a.workspaceDirty = true
}

func (a *agent) removeAttachment(index string) {
	n, err := strconv.Atoi(index)
	key := a.attachmentScope()
	files := a.attachments[key]
	if err != nil || n < 0 || n >= len(files) {
		return
	}
	files = slices.Delete(files, n, n+1)
	if len(files) == 0 {
		delete(a.attachments, key)
	} else {
		a.attachments[key] = files
	}
	a.workspaceDirty = true
}

// File references use the workspace server's paths, including for remote
// projects. Leave file reads to Codex under the selected permission policy.
func attachmentPrompt(prompt string, files []string) string {
	if len(files) == 0 {
		return prompt
	}
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\n附加文件（请读取这些文件作为本次任务的上下文）：")
	for _, path := range files {
		b.WriteString("\n- ")
		b.WriteString(strconv.Quote(path))
	}
	return b.String()
}

func (a *agent) openFiles() error {
	if err := a.closeComposerMenus(); err != nil {
		return err
	}
	directory := a.context.ProjectPath
	if directory == "" {
		var err error
		directory, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	a.filePicker = &filePicker{directory: directory, path: directory}
	return a.chooseFile(directory)
}

func (a *agent) closeFiles() error {
	if a.filePicker == nil {
		return nil
	}
	a.filePicker = nil
	return a.publisher.CloseUI("files")
}

func (a *agent) chooseFile(path string) error {
	p := a.filePicker
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.directory, path)
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			var entries []os.DirEntry
			entries, err = os.ReadDir(path)
			if err == nil {
				p.directory, p.path, p.entries = path, path, entries
			}
		} else if info.Mode().IsRegular() {
			a.addAttachment(path)
			return a.closeFiles()
		} else {
			err = fmt.Errorf("请选择普通文件")
		}
	}
	p.error = ""
	if err != nil {
		p.error = fmt.Sprintf("无法打开 %s：%v", path, err)
	}
	return a.publisher.Show(peb.UI{
		ID: "files", Title: "添加文件", Kind: peb.Dialog, Context: a.context,
		Placement: peb.UIPlacement{Width: 600}, View: a.filesView(),
	})
}

func (a *agent) handleFileEvent(e peb.EditorEvent) error {
	p := a.filePicker
	if p == nil {
		return nil
	}
	switch e.Kind {
	case "close":
		a.filePicker = nil
	case "input":
		if e.Ref == "file-path" {
			p.path = e.Value
		}
	case "submit":
		if e.Ref == "file-path-form" {
			return a.chooseFile(p.path)
		}
	case "click":
		if e.Ref == "file-parent" {
			return a.chooseFile(filepath.Dir(p.directory))
		}
		if index, ok := strings.CutPrefix(e.Ref, "file/"); ok {
			n, err := strconv.Atoi(index)
			if err == nil && n >= 0 && n < len(p.entries) {
				return a.chooseFile(filepath.Join(p.directory, p.entries[n].Name()))
			}
		}
	}
	return nil
}

func (a *agent) filesView() *peb.EditorView {
	p := a.filePicker
	var b strings.Builder
	b.WriteString(`<section class="codex-files"><form class="codex-file-path" data-ref="file-path-form"><button data-ref="file-parent" type="button" title="上一级目录" aria-label="上一级目录">..</button><input data-ref="file-path" aria-label="文件或目录路径" placeholder="输入文件或目录路径" value="`)
	b.WriteString(html.EscapeString(p.path))
	b.WriteString(`"><button type="submit">打开</button></form><p class="codex-muted">选择文件添加到本次消息，也可输入项目外文件的路径。</p>`)
	if p.error != "" {
		b.WriteString(`<p class="codex-status" role="alert">`)
		b.WriteString(html.EscapeString(p.error))
		b.WriteString(`</p>`)
	}
	b.WriteString(`<div class="codex-file-list" data-ref="file-list" data-pencode-scroll="preserve" data-pencode-scroll-key="`)
	b.WriteString(html.EscapeString(p.directory))
	b.WriteString(`">`)
	bindings := make([]peb.EditorBinding, 0, len(p.entries)+3)
	bindings = append(bindings, peb.EditorBinding{Ref: "file-path-form", Kind: "submit"}, peb.EditorBinding{Ref: "file-path", Kind: "input"}, peb.EditorBinding{Ref: "file-parent", Kind: "click"})
	for _, directory := range [...]bool{true, false} {
		for index, entry := range p.entries {
			if entry.IsDir() != directory {
				continue
			}
			ref := "file/" + strconv.Itoa(index)
			b.WriteString(`<button data-ref="`)
			b.WriteString(ref)
			b.WriteString(`" type="button">`)
			b.WriteString(html.EscapeString(entry.Name()))
			if directory {
				b.WriteByte('/')
			}
			b.WriteString(`</button>`)
			bindings = append(bindings, peb.EditorBinding{Ref: ref, Kind: "click"})
		}
	}
	if len(p.entries) == 0 {
		b.WriteString(`<p class="codex-muted">此目录为空。</p>`)
	}
	b.WriteString(`</div></section>`)
	return &peb.EditorView{HTML: b.String(), CSS: chatCSS, Events: bindings}
}
