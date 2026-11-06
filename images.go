package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"image"
	_ "image/jpeg"
	"image/png"
	"slices"
	"strconv"
	"strings"

	"github.com/pencode-labs/pencode/peb"
)

type imageAttachment struct{ url, preview string }

func (i *chatItem) renderImages(b *strings.Builder) {
	if len(i.images) == 0 {
		return
	}
	b.WriteString(`<div class="codex-message-images" aria-label="消息图片">`)
	for index, image := range i.images {
		if image.preview == "" {
			b.WriteString(`<span class="codex-muted">[图片]</span>`)
			continue
		}
		ref := "preview-message-image/" + strconv.Itoa(index) + "/" + i.data.ID
		b.WriteString(`<button class="codex-message-image" type="button" data-ref="`)
		b.WriteString(html.EscapeString(ref))
		b.WriteString(`" title="预览图片" aria-haspopup="dialog" aria-label="预览已发送图片 `)
		b.WriteString(strconv.Itoa(index + 1))
		b.WriteString(`"><img src="`)
		b.WriteString(image.preview)
		b.WriteString(`" alt="已发送图片"></button>`)
		i.linkBindings = append(i.linkBindings, peb.EditorBinding{Ref: ref, Kind: "click"})
	}
	b.WriteString(`</div>`)
}

func imagePreview(source string, limit int) (string, error) {
	if len(source) > peb.MaxDocument {
		return "", fmt.Errorf("图片过大，请粘贴小于 1.5 MiB 的图片")
	}
	header, encoded, ok := strings.Cut(source, ",")
	if !ok || header != "data:image/png;base64" && header != "data:image/jpeg;base64" {
		return "", fmt.Errorf("请粘贴 PNG 或 JPEG 图片")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("无法读取剪贴板图片：%w", err)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > 64<<20 {
		return "", fmt.Errorf("图片无效或尺寸过大")
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("无法读取剪贴板图片：%w", err)
	}
	if max(config.Width, config.Height) <= limit {
		return source, nil
	}
	return scaledImagePreview(src, limit)
}

func scaledImagePreview(src image.Image, limit int) (string, error) {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if longest := max(w, h); longest > limit {
		w, h = max(1, w*limit/longest), max(1, h*limit/longest)
		thumb := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			for x := range w {
				thumb.Set(x, y, src.At(bounds.Min.X+x*bounds.Dx()/w, bounds.Min.Y+y*bounds.Dy()/h))
			}
		}
		src = thumb
	}
	var b bytes.Buffer
	if err := png.Encode(&b, src); err != nil {
		return "", err
	}
	// Leave room for the dialog markup inside the host's document limit.
	if base64.StdEncoding.EncodedLen(b.Len())+len("data:image/png;base64,") > peb.MaxDocument-512 {
		return scaledImagePreview(src, max(w, h)*3/4)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes()), nil
}

func (a *agent) openImage(index string) error {
	n, err := strconv.Atoi(index)
	images := a.images[a.attachmentScope()]
	if err != nil || n < 0 || n >= len(images) {
		return nil
	}
	return a.showImage(images[n].url)
}

func (a *agent) openMessageImage(ref string) error {
	index, id, ok := strings.Cut(ref, "/")
	n, err := strconv.Atoi(index)
	if !ok || err != nil || n < 0 || a.selected == nil {
		return nil
	}
	for _, item := range a.selected.items {
		if item.data.ID == id && item.data.Type == "userMessage" && n < len(item.images) {
			return a.showImage(item.images[n].url)
		}
	}
	return nil
}

func (a *agent) showImage(source string) error {
	// Keep the source intact; only build the larger, host-sized preview
	// when opened so ordinary chat updates reuse the small thumbnail.
	preview, err := imagePreview(source, 1600)
	if err != nil {
		return err
	}
	return a.showImagePreview(preview)
}

func (a *agent) showImagePreview(preview string) error {
	if err := a.closeComposerMenus(); err != nil {
		return err
	}
	return a.publisher.Show(peb.UI{
		ID: "image-preview", Title: "图片预览", Kind: peb.Dialog, Context: a.context,
		Placement: peb.UIPlacement{Width: 800},
		View: &peb.EditorView{
			HTML: `<section class="codex-image-preview"><img src="` + preview + `" alt="图片预览"></section>`,
			CSS:  chatCSS,
		},
	})
}

func (a *agent) pasteImage(e peb.EditorEvent) error {
	a.setDraft(e.Value)
	if e.InputType == "error" {
		return fmt.Errorf("%s", e.Data)
	}
	if e.InputType != "image/png" {
		return nil
	}
	preview, err := imagePreview(e.Data, 160)
	if err != nil {
		return err
	}
	key := a.attachmentScope()
	if a.images == nil {
		a.images = make(map[attachmentScope][]imageAttachment)
	}
	a.images[key] = append(a.images[key], imageAttachment{e.Data, preview})
	a.dismissCompletion()
	a.workspaceDirty = true
	return nil
}

func (a *agent) removeImage(index string) {
	n, err := strconv.Atoi(index)
	key := a.attachmentScope()
	images := a.images[key]
	if err != nil || n < 0 || n >= len(images) {
		return
	}
	images = slices.Delete(images, n, n+1)
	if len(images) == 0 {
		delete(a.images, key)
	} else {
		a.images[key] = images
	}
	a.workspaceDirty = true
}
