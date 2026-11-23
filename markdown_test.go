package main

import (
	"html"
	"regexp"
	"strings"
	"testing"
)

func TestChatMarkdownStrong(t *testing.T) {
	for _, tc := range []struct {
		source string
		want   string
	}{
		{"**安全变化：**保护方式改为文件权限。", "<p><strong>安全变化：</strong>保护方式改为文件权限。</p>\n"},
		{"这是**重要**提示。", "<p>这是<strong>重要</strong>提示。</p>\n"},
		{"请看**“安全变化”**说明。", "<p>请看<strong>“安全变化”</strong>说明。</p>\n"},
		{"**bold** and __strong__", "<p><strong>bold</strong> and <strong>strong</strong></p>\n"},
		{"***重点***", "<p><em><strong>重点</strong></em></p>\n"},
		{"**安全 `key`：**保护", "<p><strong>安全 <code>key</code>：</strong>保护</p>\n"},
		{"`**普通文本**`", "<p><code>**普通文本**</code></p>\n"},
		{`\*\*普通文本\*\*`, "<p>**普通文本**</p>\n"},
		{"**未结束", "<p>**未结束</p>\n"},
		{"** 空白 **", "<p>** 空白 **</p>\n"},
		{"file_name_here", "<p>file_name_here</p>\n"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			var b strings.Builder
			renderMarkdown(&b, tc.source, chatMarkdownState{scope: "message"})
			if got := b.String(); got != tc.want {
				t.Fatalf("renderMarkdown() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestChatFencedCodeBlocks(t *testing.T) {
	const code = "  echo \"$HOME\"  # <tag> & 'text'\n\n\tprintf ok\n"
	var b strings.Builder
	renderMarkdown(&b, "`inline`\n\n```sh example\n"+code+"```\n\n~~~\n**普通文本**\n~~~\n\n```x-unknown<&\n<literal>\n```\n\n    indented", chatMarkdownState{scope: "message"})
	markup := b.String()
	for _, want := range []string{
		"<code>inline</code>", ">Bash</span>", ">代码</span>", ">x-unknown&lt;&amp;</span>",
		`data-pencode-copy="` + html.EscapeString(code) + `"`, `class="tok-variable"`, `class="tok-comment"`,
		"<pre><code>**普通文本**\n</code></pre>", "<pre><code>&lt;literal&gt;\n</code></pre>", "<pre><code>indented\n</code></pre>",
	} {
		if !strings.Contains(markup, want) {
			t.Errorf("missing %q in %s", want, markup)
		}
	}
	_, body, _ := strings.Cut(markup, "<pre><code>")
	body, _, _ = strings.Cut(body, "</code>")
	if got := html.UnescapeString(regexp.MustCompile(`</?span[^>]*>`).ReplaceAllString(body, "")); got != code {
		t.Fatalf("highlighted text = %q, want %q", got, code)
	}
}

func TestChatCodeBlockWrapDuringStream(t *testing.T) {
	i := newChatItem(threadItem{Type: "agentMessage", ID: "reply", Text: "```go\nvar a = 1\n```\n\n```bash\necho"}, false)
	c := &conversation{items: []*chatItem{i}}
	a := &agent{selected: c}
	view := a.composerView()
	validateBindings(t, view)
	var wrapRefs []string
	for _, event := range view.Events {
		if strings.HasPrefix(event.Ref, "wrap-code/") {
			wrapRefs = append(wrapRefs, event.Ref)
		}
	}
	if len(wrapRefs) != 2 {
		t.Fatalf("streamed block controls = %v", wrapRefs)
	}
	a.toggleTranscript(wrapRefs[1])
	i.replace(threadItem{Type: "agentMessage", ID: "reply", Text: i.text.String() + " done\n```"}, true)
	view = a.composerView()
	validateBindings(t, view)
	if strings.Count(view.HTML, `class="codex-code-wrapped"`) != 1 || !strings.Contains(view.HTML, "aria-pressed=\"true\"") || !strings.Contains(view.HTML, "data-pencode-copy=\"echo done\n\"") {
		t.Fatal("completed block lost its contents or wrap selection", i.html)
	}
	a.toggleTranscript(wrapRefs[1])
	if strings.Contains(a.composerView().HTML, `class="codex-code-wrapped"`) {
		t.Fatal("wrap toggle did not restore horizontal scrolling")
	}
}
