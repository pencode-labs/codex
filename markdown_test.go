package main

import (
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
		{"```text\n**普通文本**\n```", "<pre><code class=\"language-text\">**普通文本**\n</code></pre>\n"},
		{`\*\*普通文本\*\*`, "<p>**普通文本**</p>\n"},
		{"**未结束", "<p>**未结束</p>\n"},
		{"** 空白 **", "<p>** 空白 **</p>\n"},
		{"file_name_here", "<p>file_name_here</p>\n"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			var b strings.Builder
			renderMarkdown(&b, tc.source, "", "message")
			if got := b.String(); got != tc.want {
				t.Fatalf("renderMarkdown() = %q, want %q", got, tc.want)
			}
		})
	}
}
