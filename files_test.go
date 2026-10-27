package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pencode-labs/pencode/peb"
)

func TestAttachmentsFollowDraftScope(t *testing.T) {
	a := &agent{context: peb.ActionContext{ProjectPath: "one"}}
	a.addAttachment("first.go")
	a.addAttachment("first.go")
	if len(a.attachments[a.attachmentScope()]) != 1 {
		t.Fatal("duplicate attachment")
	}
	a.selected = &conversation{thread: thread{ID: "saved"}}
	if len(a.attachments[a.attachmentScope()]) != 0 {
		t.Fatal("new draft attachment leaked into a conversation")
	}
	a.addAttachment("saved.go")
	a.selected = nil
	a.context.ProjectPath = "two"
	if len(a.attachments[a.attachmentScope()]) != 0 {
		t.Fatal("attachment leaked into another project")
	}
	a.context.ProjectPath = "one"
	view := a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, "first.go") || strings.Contains(view.HTML, "saved.go") {
		t.Fatal("draft attachment was not restored", view.HTML)
	}
	if n := testing.AllocsPerRun(100, func() {
		a.addAttachment("first.go")
		attachmentPrompt("task", nil)
	}); n != 0 {
		t.Fatalf("unchanged attachments allocate: %g", n)
	}
	a.removeAttachment("-1")
	a.removeAttachment("1")
	if len(a.attachments[a.attachmentScope()]) != 1 {
		t.Fatal("invalid removal changed attachments")
	}
	a.removeAttachment("0")
	if _, ok := a.attachments[a.attachmentScope()]; ok {
		t.Fatal("empty attachment draft retained")
	}
}

func TestFilePickerAndAttachmentsEscapeNames(t *testing.T) {
	directory := t.TempDir()
	name := "notes & context.txt"
	if err := os.WriteFile(filepath.Join(directory, name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "source"), 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	a := &agent{filePicker: &filePicker{directory: directory, path: `<script>`, entries: entries}}
	view := a.filesView()
	validateBindings(t, view)
	if strings.Contains(view.HTML, "<script>") || !strings.Contains(view.HTML, "notes &amp; context.txt") || strings.Index(view.HTML, "source/") > strings.Index(view.HTML, "notes &amp; context.txt") {
		t.Fatal("picker does not escape names or place directories first", view.HTML)
	}
	path := filepath.Join(directory, name)
	a.addAttachment(path)
	view = a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, "notes &amp; context.txt") || strings.Contains(view.HTML, "composer-menu") || !strings.Contains(view.HTML, `data-ref="add-file"`) {
		t.Fatal(view.HTML)
	}
	if prompt := attachmentPrompt("review", []string{path, "line\nbreak.go"}); !strings.HasPrefix(prompt, "review\n\n附加文件") || !strings.Contains(prompt, `line\nbreak.go`) {
		t.Fatal("file references were not included or quoted", prompt)
	}
}
