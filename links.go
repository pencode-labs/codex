package main

import (
	"html"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pencode-labs/pencode/peb"
	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/renderer"
	markdownhtml "github.com/yuin/goldmark/v2/renderer/html"
	"github.com/yuin/goldmark/v2/util"
)

var chatLinkContext = renderer.NewContextKey()

type chatLinkState struct {
	root, scope string
	index       int
	bindings    []peb.EditorBinding
}

func chatLinkDecorator(markdownhtml.NodeRenderer) markdownhtml.NodeRenderer {
	return markdownhtml.NodeRendererFunc(renderChatLink)
}

func renderChatLink(writer io.Writer, source []byte, node ast.Node, entering bool, context renderer.Context) (ast.WalkStatus, error) {
	w := writer.(util.BufWriter)
	if !entering {
		if node.Kind() == ast.KindLink {
			_, _ = w.WriteString(`</button>`)
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
	links := context.Get(chatLinkContext).(*chatLinkState)
	href, icon, directory := chatLinkTarget(destination, links.root)
	var ref string
	_, _ = w.WriteString(`<button type="button" role="link"`)
	if href != "" {
		ref = "link/" + links.scope + "/" + strconv.Itoa(links.index)
		links.index++
		if directory != "" {
			ref = "directory/" + url.PathEscape(directory) + "/" + ref
			links.bindings = append(links.bindings, peb.EditorBinding{Ref: ref, Kind: "click"})
		}
		_, _ = w.WriteString(` class="codex-link" data-ref="` + html.EscapeString(ref) + `" data-pencode-href="` + html.EscapeString(href) + `" title="`)
		if title == "" {
			title = destination
		}
		_, _ = w.WriteString(html.EscapeString(title) + `"`)
	}
	_, _ = w.WriteString(`>`)
	if href != "" {
		_, _ = w.WriteString(`<img class="codex-link-icon" data-ref="` + html.EscapeString(ref+"/icon") + `" data-pencode-icon="` + icon + `" alt="" aria-hidden="true">`)
	}
	if n, ok := node.(*ast.AutoLink); ok {
		_, _ = n.Label.WriteTo(markdownhtml.ContextTextWriter(context), source)
		_, _ = w.WriteString(`</button>`)
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
	ui := peb.UI{ID: "directory/" + name, Title: filepath.Base(name), Kind: peb.EditorTab, Context: context, View: directoryView(name)}
	if err := a.publisher.Show(ui); err != nil {
		return err
	}
	ui.View = nil
	return a.publisher.Show(ui)
}

func directoryView(name string) *peb.EditorView {
	entries, err := os.ReadDir(name)
	var b strings.Builder
	b.WriteString(`<section class="codex-directory"><h2>` + html.EscapeString(filepath.Base(name)) + `</h2><p class="codex-muted">` + html.EscapeString(name) + `</p>`)
	var bindings []peb.EditorBinding
	if err != nil {
		b.WriteString(`<p class="codex-status" role="alert">` + html.EscapeString(err.Error()) + `</p>`)
	} else {
		b.WriteString(`<nav aria-label="目录内容">`)
		bindings = directoryEntry(&b, bindings, filepath.Dir(name), "..", "parent", true)
		for _, directory := range [...]bool{true, false} {
			for index, entry := range entries {
				if entry.IsDir() == directory {
					bindings = directoryEntry(&b, bindings, filepath.Join(name, entry.Name()), entry.Name(), strconv.Itoa(index), directory)
				}
			}
		}
		if len(entries) == 0 {
			b.WriteString(`<p class="codex-muted">此目录为空。</p>`)
		}
		b.WriteString(`</nav>`)
	}
	b.WriteString(`</section>`)
	return &peb.EditorView{HTML: b.String(), CSS: chatCSS, Events: bindings}
}

func directoryEntry(b *strings.Builder, bindings []peb.EditorBinding, name, label, ref string, directory bool) []peb.EditorBinding {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(name)}
	_, icon, _ := chatLinkTarget(u.String(), "")
	if directory {
		ref, icon = "directory/"+url.PathEscape(name)+"/"+ref, "folder"
		bindings = append(bindings, peb.EditorBinding{Ref: ref, Kind: "click"})
	}
	b.WriteString(`<button type="button" role="link" class="codex-link" data-ref="` + html.EscapeString(ref) + `" data-pencode-href="` + html.EscapeString(u.String()) + `"><img class="codex-link-icon" data-ref="` + html.EscapeString(ref+"/icon") + `" data-pencode-icon="` + icon + `" alt="" aria-hidden="true">` + html.EscapeString(label) + `</button>`)
	return bindings
}
