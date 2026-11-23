package main

import (
	"html"
	"io"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/pencode-labs/pencode/peb"
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/renderer"
	markdownhtml "github.com/yuin/goldmark/v2/renderer/html"
)

func chatCodeBlockDecorator(next markdownhtml.NodeRenderer) markdownhtml.NodeRenderer {
	return markdownhtml.NodeRendererFunc(func(w io.Writer, source []byte, node ast.Node, entering bool, ctx renderer.Context) (ast.WalkStatus, error) {
		block := node.(*ast.CodeBlock)
		if block.CodeBlockKind != ast.CodeBlockKindFenced {
			return next.Render(w, source, node, entering, ctx)
		}
		if entering {
			state := ctx.Get(chatMarkdownContext).(*chatMarkdownState)
			language, _ := block.Language(source)
			state.writeCodeBlock(w.(io.StringWriter), language, block.Value.Str(source))
		}
		return ast.WalkContinue, nil
	})
}

func (s *chatMarkdownState) writeCodeBlock(w io.StringWriter, language, code string) {
	id := s.scope + "/" + strconv.Itoa(s.codeIndex)
	s.codeIndex++
	wrapRef := "wrap-code/" + id
	wrapped := s.wrapped[wrapRef]
	s.bindings = append(s.bindings, peb.EditorBinding{Ref: wrapRef, Kind: "click"})
	id = html.EscapeString(id)

	label := language
	var lexer chroma.Lexer
	if language == "" {
		label = "代码"
	} else if lexer = lexers.Get(language); lexer != nil {
		label = lexer.Config().Name
		if label == "plaintext" {
			label, lexer = "纯文本", nil
		}
	}
	escaped := html.EscapeString(code)
	w.WriteString(`<div class="codex-code-block"><div class="codex-code-header"><img alt="" data-pencode-icon="code" data-ref="code-icon/` + id + `"><span>`)
	w.WriteString(html.EscapeString(label))
	w.WriteString(`</span><button type="button" class="codex-icon-button" data-ref="wrap-code/` + id + `" title="自动换行" aria-label="自动换行" aria-pressed="` + strconv.FormatBool(wrapped) + `"><img src="resources/icons/wrap-text.svg" alt=""></button>`)
	w.WriteString(`<button type="button" class="codex-icon-button" data-ref="copy-code/` + id + `" title="复制代码" aria-label="复制代码" data-pencode-copy="`)
	w.WriteString(escaped)
	w.WriteString(`"><img alt="" data-pencode-icon="copy" data-ref="copy-code-icon/` + id + `"></button></div>`)
	if wrapped {
		w.WriteString(`<pre class="codex-code-wrapped">`)
	} else {
		w.WriteString("<pre>")
	}
	w.WriteString("<code>")
	w.WriteString(highlightCode(lexer, code, escaped))
	w.WriteString("</code></pre></div>\n")
}

func highlightCode(lexer chroma.Lexer, code, escaped string) (markup string) {
	// Keep token markup within the transcript's per-message display budget.
	const budget = 64 << 10
	if lexer == nil || len(code) > budget {
		return escaped
	}
	// Chroma iterators may report a lexer error by panicking mid-stream.
	defer func() {
		if recover() != nil {
			markup = escaped
		}
	}()
	tokens, err := lexer.Tokenise(&chroma.TokeniseOptions{State: "root", Nested: true}, code)
	if err != nil {
		return escaped
	}
	var b strings.Builder
	active := ""
	for token := range tokens.Stdlib() {
		class := codeTokenClass(token.Type)
		if active != class {
			if active != "" {
				b.WriteString("</span>")
			}
			if class != "" {
				b.WriteString(`<span class="` + class + `">`)
			}
			active = class
		}
		b.WriteString(html.EscapeString(token.Value))
		if b.Len() > budget {
			return escaped
		}
	}
	if active != "" {
		b.WriteString("</span>")
	}
	return b.String()
}

func codeTokenClass(token chroma.TokenType) string {
	for ; token > 0; token = token.Parent() {
		switch token {
		case chroma.Keyword, chroma.NameTag:
			return "tok-keyword"
		case chroma.KeywordType, chroma.NameClass, chroma.NameBuiltin:
			return "tok-type"
		case chroma.NameFunction:
			return "tok-function"
		case chroma.NameVariable, chroma.NameAttribute:
			return "tok-variable"
		case chroma.LiteralString:
			return "tok-string"
		case chroma.LiteralNumber:
			return "tok-number"
		case chroma.Comment:
			return "tok-comment"
		case chroma.Operator:
			return "tok-operator"
		}
	}
	return ""
}
