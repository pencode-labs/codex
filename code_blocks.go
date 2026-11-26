package main

import (
	"io"
	"strconv"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/onix-engine/onix/html/markup"
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
	b := markup.Acquire()
	defer b.Release()

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
	b.Open("div", "class", "codex-code-block")
	b.Open("div", "class", "codex-code-header")
	b.Element("img", "alt", "", "data-pencode-icon", "code", "data-ref", "code-icon/"+id)
	b.TextElement("span", label)
	b.Open("button", "type", "button", "class", "codex-icon-button", "data-ref", wrapRef, "title", "自动换行", "aria-label", "自动换行", "aria-pressed", strconv.FormatBool(wrapped))
	b.Element("img", "src", "resources/icons/wrap-text.svg", "alt", "")
	b.End()
	b.Open("button", "type", "button", "class", "codex-icon-button", "data-ref", "copy-code/"+id, "title", "复制代码", "aria-label", "复制代码", "data-pencode-copy", code)
	b.Element("img", "alt", "", "data-pencode-icon", "copy", "data-ref", "copy-code-icon/"+id)
	b.End()
	b.End()
	pre := b.Open("pre")
	if wrapped {
		pre.SetAttr("class", "codex-code-wrapped")
	}
	b.Open("code")
	if highlighted := highlightCode(lexer, code); highlighted != "" {
		b.Embed(highlighted)
	} else {
		b.Text(code)
	}
	b.End()
	b.End()
	b.End()
	b.Text("\n")
	w.WriteString(b.String())
}

func highlightCode(lexer chroma.Lexer, code string) (fragment string) {
	// Keep token markup within the transcript's per-message display budget.
	const budget = 64 << 10
	if lexer == nil || len(code) > budget {
		return ""
	}
	// Chroma iterators may report a lexer error by panicking mid-stream.
	defer func() {
		if recover() != nil {
			fragment = ""
		}
	}()
	tokens, err := lexer.Tokenise(&chroma.TokeniseOptions{State: "root", Nested: true}, code)
	if err != nil {
		return ""
	}
	b := markup.Acquire()
	defer b.Release()
	active := ""
	for token := range tokens.Stdlib() {
		class := codeTokenClass(token.Type)
		if active != class {
			if active != "" {
				b.End()
			}
			if class != "" {
				b.Open("span", "class", class)
			}
			active = class
		}
		b.Text(token.Value)
		if b.SizeHint() > budget {
			return ""
		}
	}
	if active != "" {
		b.End()
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
