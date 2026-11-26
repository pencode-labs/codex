package main

import (
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/onix-engine/onix/html/markup"
	"github.com/pencode-labs/pencode/peb"
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/renderer"
	markdownhtml "github.com/yuin/goldmark/v2/renderer/html"
	"github.com/yuin/goldmark/v2/util"
)

func chatLinkDecorator(markdownhtml.NodeRenderer) markdownhtml.NodeRenderer {
	return markdownhtml.NodeRendererFunc(renderChatLink)
}

func renderChatLink(writer io.Writer, source []byte, node ast.Node, entering bool, context renderer.Context) (ast.WalkStatus, error) {
	w := writer.(util.BufWriter)
	if !entering {
		if node.Kind() == ast.KindLink {
			_, err := w.WriteString(markup.EndTag("a"))
			return ast.WalkContinue, err
		}
		return ast.WalkContinue, nil
	}
	var destination, title string
	switch n := node.(type) {
	case *ast.Link:
		destination, title = n.Destination.Value(source), n.Title.Value(source)
	case *ast.AutoLink:
		destination = n.Destination.Value(source)
	}
	links := context.Get(chatMarkdownContext).(*chatMarkdownState)
	href, icon, directory := chatLinkTarget(destination, links.root)
	var ref string
	b := markup.Acquire()
	defer b.Release()
	link := b.Open("a", "role", "link")
	if href != "" {
		ref = "link/" + links.scope + "/" + strconv.Itoa(links.linkIndex)
		links.linkIndex++
		if directory != "" {
			ref = "directory/" + url.PathEscape(directory) + "/" + ref
			links.bindings = append(links.bindings, peb.EditorBinding{Ref: ref, Kind: "click"})
		}
		if title == "" {
			title = destination
		}
		link.SetAttr("class", "codex-link")
		link.SetAttr("data-ref", ref)
		link.SetAttr("href", href)
		link.SetAttr("title", title)
	}
	if _, err := w.WriteString(markup.StartTag(link)); err != nil {
		return ast.WalkStop, err
	}
	if href != "" {
		iconNode := b.Element("img", "class", "codex-link-icon", "data-ref", ref+"/icon", "data-pencode-icon", icon, "alt", "", "aria-hidden", "true")
		if _, err := w.WriteString(markup.HTML(iconNode)); err != nil {
			return ast.WalkStop, err
		}
	}

	if n, ok := node.(*ast.AutoLink); ok {
		_, _ = n.Label.WriteTo(markdownhtml.ContextTextWriter(context), source)
		_, err := w.WriteString(markup.EndTag("a"))
		return ast.WalkContinue, err
	}
	return ast.WalkContinue, nil
}

// Resolve paths once when a changed message is rendered. Cached messages never
// stat files again while new tokens or unrelated history updates arrive.
func chatLinkTarget(destination, root string) (href, icon, directory string) {
	u, err := url.Parse(destination)
	if err != nil {
		if colon := strings.LastIndexByte(destination, ':'); colon >= 0 {
			if _, numberErr := strconv.Atoi(destination[colon+1:]); numberErr == nil {
				u, err = url.Parse("./" + destination)
			}
		}
	}
	if err != nil {
		return
	}
	// A colon in the first path segment otherwise looks like a URI scheme.
	if u.Opaque != "" {
		if _, err := strconv.Atoi(strings.ReplaceAll(u.Opaque, ":", "")); err == nil {
			u.Path, u.Scheme, u.Opaque = "./"+u.Scheme+":"+u.Opaque, "", ""
		}
	}
	if u.Scheme == "" && u.Host != "" {
		u.Scheme = "https"
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "mailto":
		return u.String(), "globe", ""
	case "", "file":
		if u.Path == "" || u.Host != "" && u.Host != "localhost" {
			return
		}
	default:
		return
	}
	name := u.Path
	// A line reference is a code link even for extensionless source files.
	code := u.Fragment != ""
	if index := strings.LastIndexByte(name, ':'); index > 0 {
		if line, err := strconv.Atoi(name[index+1:]); err == nil && line > 0 {
			code = true
			name = name[:index]
			if index := strings.LastIndexByte(name, ':'); index > 0 {
				if _, err := strconv.Atoi(name[index+1:]); err == nil {
					name = name[:index]
				}
			}
		}
	}
	if !filepath.IsAbs(name) && root != "" {
		name = filepath.Join(root, name)
	}
	icon = "file-text"
	if strings.HasSuffix(u.Path, "/") {
		directory = name
	} else if info, err := os.Stat(name); err == nil && info.IsDir() {
		directory = name
	}
	if directory != "" {
		return u.String(), "folder", directory
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go", ".rs", ".c", ".h", ".cpp", ".hpp", ".cs", ".java", ".kt", ".swift", ".m", ".mm", ".py", ".rb", ".php", ".js", ".jsx", ".ts", ".tsx", ".vue", ".svelte", ".html", ".css", ".scss", ".sh", ".sql", ".lua", ".zig":
		code = true
	}
	if code {
		icon = "code"
	}
	return u.String(), icon, ""
}

func (a *agent) openDirectoryLink(encoded string, context peb.ActionContext) error {
	name, err := url.PathUnescape(encoded)
	if err != nil {
		return err
	}
	ui := peb.UI{ID: previewID("directory", name), Title: filepath.Base(name), Kind: peb.EditorTab, Context: context, View: directoryView(name)}
	if err := a.publisher.Show(ui); err != nil {
		return err
	}
	ui.View = nil
	return a.publisher.Show(ui)
}

func directoryView(name string) *peb.EditorView {
	entries, err := os.ReadDir(name)
	b := markup.Acquire()
	defer b.Release()
	b.Open("section", "class", "codex-directory")
	b.TextElement("h2", filepath.Base(name))
	b.TextElement("p", name, "class", "codex-muted")
	var bindings []peb.EditorBinding
	if err != nil {
		b.TextElement("p", err.Error(), "class", "codex-status", "role", "alert")
	} else {
		b.Open("nav", "aria-label", "目录内容")
		bindings = directoryEntry(b, bindings, filepath.Dir(name), "..", "parent", true)
		for _, directory := range [...]bool{true, false} {
			for index, entry := range entries {
				if entry.IsDir() == directory {
					bindings = directoryEntry(b, bindings, filepath.Join(name, entry.Name()), entry.Name(), strconv.Itoa(index), directory)
				}
			}
		}
		if len(entries) == 0 {
			b.TextElement("p", "此目录为空。", "class", "codex-muted")
		}
		b.End()
	}
	b.End()
	return &peb.EditorView{HTML: b.String(), CSS: chatCSS, Events: bindings}
}

func directoryEntry(b *markup.Builder, bindings []peb.EditorBinding, name, label, ref string, directory bool) []peb.EditorBinding {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(name)}
	_, icon, _ := chatLinkTarget(u.String(), "")
	if directory {
		ref, icon = "directory/"+url.PathEscape(name)+"/"+ref, "folder"
		bindings = append(bindings, peb.EditorBinding{Ref: ref, Kind: "click"})
	}
	b.Open("button", "type", "button", "role", "link", "class", "codex-link", "data-ref", ref, "data-pencode-href", u.String())
	b.Element("img", "class", "codex-link-icon", "data-ref", ref+"/icon", "data-pencode-icon", icon, "alt", "", "aria-hidden", "true")
	b.Text(label)
	b.End()
	return bindings
}
