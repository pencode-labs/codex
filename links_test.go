package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pencode-labs/pencode/peb"
)

func TestChatLinksRenderTargetsAndIcons(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	bindings := renderMarkdown(&b, "[目录](docs) [说明](guide%20notes.md) [`index.html`](index.html:12) [网站](https://example.com?q=1&x=2) https://example.org\n\n[危险](javascript:alert%281%29) ![隐藏](https://example.com/secret)", root, "message")
	view := &peb.EditorView{HTML: b.String(), Events: bindings}
	validateBindings(t, view)
	for _, want := range []string{`data-pencode-icon="folder"`, `data-pencode-icon="file-text"`, `data-pencode-icon="code"`, `data-pencode-icon="globe"`, `data-pencode-href="./index.html:12"`, `data-pencode-href="https://example.com?q=1&amp;x=2"`, `data-pencode-href="https://example.org"`, `role="link"`, `<code>index.html</code>`} {
		if !strings.Contains(view.HTML, want) {
			t.Fatalf("missing %s in %s", want, view.HTML)
		}
	}
	if len(bindings) != 1 || !strings.HasPrefix(bindings[0].Ref, "directory/") || strings.Contains(view.HTML, `data-pencode-href="javascript:`) || strings.Contains(view.HTML, "secret") {
		t.Fatal("incorrect navigation bindings or unsafe resource", view)
	}
}

func TestChatLinkRefsSurvivePaginationAndCachedRenders(t *testing.T) {
	item := newChatItem(threadItem{ID: "message", Type: "agentMessage", Text: "[file](main.go) [folder](docs/)"}, true)
	c := &conversation{items: []*chatItem{item}}
	a := &agent{selected: c}
	first := a.composerView()
	fragment := item.html
	c.items = append([]*chatItem{newChatItem(threadItem{ID: "older", Type: "agentMessage", Text: "[file](main.go)"}, true)}, c.items...)
	view := a.composerView()
	validateBindings(t, view)
	if item.html != fragment || !strings.Contains(first.HTML, fragment) || !strings.Contains(view.HTML, fragment) || len(item.linkBindings) != 1 {
		t.Fatal("pagination changed cached links")
	}
}

func TestUserMessagesRenderClickableLinks(t *testing.T) {
	item := newChatItem(threadItem{ID: "user", Type: "userMessage", Content: []input{{Type: "text", Text: "请查看 [项目](extensions/codex/) 和 https://example.com"}}}, true)
	a := &agent{selected: &conversation{items: []*chatItem{item}}}
	view := a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, `data-pencode-href="extensions/codex/"`) || !strings.Contains(view.HTML, `data-pencode-href="https://example.com"`) {
		t.Fatal("user message links are not interactive", view.HTML)
	}
}

func TestDirectoryLinksBrowseEscapedEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "source & tests"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "guide <notes>.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	view := directoryView(root)
	validateBindings(t, view)
	if len(view.Events) != 2 || !strings.Contains(view.HTML, "source &amp; tests") || !strings.Contains(view.HTML, "guide &lt;notes&gt;.md") || !strings.Contains(view.HTML, "guide%20%3Cnotes%3E.md") || strings.Index(view.HTML, "source &amp; tests") > strings.Index(view.HTML, "guide &lt;notes&gt;.md") {
		t.Fatal("directory links lost escaping or ordering", view)
	}
	view = directoryView(filepath.Join(root, "missing"))
	if !strings.Contains(view.HTML, `role="alert"`) {
		t.Fatal("directory errors must be visible", view)
	}
}
