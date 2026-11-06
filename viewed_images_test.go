package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"html"
	"image"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pencode-labs/pencode/peb"
)

func writeViewedImage(t *testing.T, path string) {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(testImageURL(t), "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestViewedImagesHistoryAndStream(t *testing.T) {
	for _, source := range []string{"history", "stream"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, `one"<&.png`)
			c := &conversation{thread: thread{ID: "thread"}, context: peb.ActionContext{ProjectPath: root}, draft: "保留草稿"}
			updates := make(chan peb.UI, 2)
			a := &agent{selected: c, context: c.context, conversations: map[string]*conversation{c.ID: c}, publisher: syncPublisher{updates: updates}}
			add := func(item threadItem) {
				t.Helper()
				if source == "history" {
					c.items = append(c.items, newChatItem(item, true))
					return
				}
				params, err := json.Marshal(map[string]any{"threadId": c.ID, "item": item})
				if err != nil {
					t.Fatal(err)
				}
				for _, method := range []string{"item/started", "item/completed"} {
					a.onMessage(envelope{Method: method, Params: params})
				}
			}
			add(threadItem{ID: `first"<&`, Type: "imageView", Path: filepath.Base(path)})
			view := a.composerView()
			validateBindings(t, view)
			if !strings.Contains(view.HTML, "已查看 1 张图像") || len(c.items[0].images) != 0 {
				t.Fatal("collapsed image summary loaded a thumbnail or lost its count")
			}
			// The image is created after the collapsed render to verify lazy loading.
			writeViewedImage(t, path)
			writeViewedImage(t, filepath.Join(root, "two.png"))
			add(threadItem{ID: "second", Type: "imageView", Path: filepath.Join(root, "two.png")})
			view = a.composerView()
			validateBindings(t, view)
			if strings.Count(view.HTML, `class="codex-image-views"`) != 1 || !strings.Contains(view.HTML, "已查看 2 张图像") || strings.Contains(view.HTML, `data-ref="view-image/`) {
				t.Fatal("consecutive images should share one collapsed summary")
			}
			first := c.items[0]
			clickActivity(t, a, first.groupRef)
			view = a.composerView()
			validateBindings(t, view)
			if strings.Count(view.HTML, `class="codex-viewed-image"`) != 2 || !strings.Contains(view.HTML, html.EscapeString(filepath.Base(path))) || !strings.Contains(view.HTML, "data:image/png;base64,") {
				t.Fatal("expanded summary lost thumbnails or path escaping")
			}
			preview := first.images[0].preview
			clickActivity(t, a, "view-image/"+first.data.ID)
			if len(updates) != 1 {
				t.Fatal("thumbnail did not open a preview")
			}
			ui := <-updates
			if ui.ID != "image-preview" || ui.Kind != peb.Dialog || ui.Context != a.context || c.draft != "保留草稿" {
				t.Fatal("preview lost dialog context or changed the draft")
			}
			validateBindings(t, ui.View)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			// An authoritative snapshot retains an unchanged image's cached thumbnail.
			first.replace(first.data, true)
			clickActivity(t, a, first.groupRef)
			view = a.composerView()
			validateBindings(t, view)
			if strings.Contains(view.HTML, `data-ref="view-image/`) {
				t.Fatal("collapsed summary retained thumbnail controls")
			}
			clickActivity(t, a, first.groupRef)
			view = a.composerView()
			validateBindings(t, view)
			if first.images[0].preview != preview || !strings.Contains(view.HTML, preview) {
				t.Fatal("refresh discarded a cached thumbnail")
			}
			add(threadItem{ID: "reply", Type: "agentMessage", Text: "继续查看"})
			add(threadItem{ID: "third", Type: "imageView", Path: "two.png"})
			view = a.composerView()
			validateBindings(t, view)
			if strings.Count(view.HTML, `class="codex-image-views"`) != 2 || !strings.Contains(view.HTML, "已查看 1 张图像") {
				t.Fatal("messages must separate image groups")
			}
		})
	}
}

func TestViewedImageMissingAndInvalidSources(t *testing.T) {
	root := t.TempDir()
	i := newChatItem(threadItem{ID: "missing", Type: "imageView", Path: "missing.png"}, true)
	c := &conversation{context: peb.ActionContext{ProjectPath: root}, items: []*chatItem{i}}
	a := &agent{selected: c}
	_ = a.composerView()
	clickActivity(t, a, i.groupRef)
	view := a.composerView()
	validateBindings(t, view)
	if !strings.Contains(view.HTML, "图片不可用") {
		t.Fatal("missing images need a visible fallback")
	}
	if err := a.openViewedImage("missing"); err == nil || !strings.Contains(err.Error(), "无法预览图片") {
		t.Fatal("missing image preview must report an error", err)
	}
	if err := a.openViewedImage("unknown"); err != nil {
		t.Fatal("unknown IDs must be ignored", err)
	}
	writeViewedImage(t, filepath.Join(root, "valid.png"))
	item := i.data
	item.Path = "valid.png"
	i.replace(item, true)
	view = a.composerView()
	if strings.Contains(view.HTML, "图片不可用") || !strings.Contains(view.HTML, "data:image/png;base64,") {
		t.Fatal("a changed path must invalidate the old thumbnail")
	}
	for _, path := range []string{"", "missing.png", root} {
		if _, err := localImagePreview(path, root, 160); err == nil {
			t.Fatalf("invalid image source %q was accepted", path)
		}
	}
	invalid := filepath.Join(root, "invalid.png")
	if err := os.WriteFile(invalid, []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := localImagePreview(invalid, root, 160); err == nil {
		t.Fatal("invalid image bytes were accepted")
	}
}

func TestLocalImagePreviewScalesPNGAndJPEG(t *testing.T) {
	for _, format := range []string{"png", "jpeg"} {
		t.Run(format, func(t *testing.T) {
			src := image.NewNRGBA(image.Rect(0, 0, 2000, 500))
			var encoded bytes.Buffer
			var err error
			if format == "png" {
				err = png.Encode(&encoded, src)
			} else {
				err = jpeg.Encode(&encoded, src, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "image."+format)
			if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			for _, limit := range []int{160, 1600} {
				preview, err := localImagePreview(path, "", limit)
				if err != nil {
					t.Fatal(err)
				}
				data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(preview, "data:image/png;base64,"))
				if err != nil {
					t.Fatal(err)
				}
				config, err := png.DecodeConfig(bytes.NewReader(data))
				if err != nil || config.Width != limit || config.Height != limit/4 {
					t.Fatal("preview lost aspect ratio or exceeded its size limit", config, err)
				}
			}
		})
	}
}

func TestViewedImagePreviewFitsHostDocument(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 1200, 1200))
	random := rand.New(rand.NewPCG(1, 2))
	for index := range src.Pix {
		src.Pix[index] = byte(random.Uint32())
	}
	preview, err := scaledImagePreview(src, 1600)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview) > peb.MaxDocument-512 {
		t.Fatal("noisy images must fit inside the host's preview dialog")
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(preview, "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Width >= 1200 || config.Width != config.Height {
		t.Fatal("bounded preview lost its aspect ratio", config, err)
	}
}
