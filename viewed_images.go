package main

import (
	"fmt"
	"html"
	"image"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pencode-labs/pencode/peb"
)

func localImagePreview(path, root string, limit int) (string, error) {
	if path == "" {
		return "", fmt.Errorf("图片路径为空")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return "", fmt.Errorf("图片无效或文件过大")
	}
	r := io.NewSectionReader(f, 0, info.Size())
	config, _, err := image.DecodeConfig(r)
	if err != nil || config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > 64<<20 {
		return "", fmt.Errorf("图片无效或尺寸过大")
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	src, _, err := image.Decode(r)
	if err != nil {
		return "", err
	}
	return scaledImagePreview(src, limit)
}

func (a *agent) openViewedImage(id string) error {
	if a.selected == nil {
		return nil
	}
	for _, i := range a.selected.items {
		if i.data.ID != id || i.data.Type != "imageView" {
			continue
		}
		preview, err := localImagePreview(i.data.Path, a.selected.context.ProjectPath, 1600)
		if err != nil {
			return fmt.Errorf("无法预览图片：%w", err)
		}
		return a.showImagePreview(preview)
	}
	return nil
}

func renderViewedImages(b *strings.Builder, bindings []peb.EditorBinding, items []*chatItem, root string) []peb.EditorBinding {
	first := items[0]
	if first.groupRef == "" {
		first.groupRef = "activity/" + first.data.ID
	}
	b.WriteString(`<section class="codex-image-views" data-ref="`)
	b.WriteString(html.EscapeString(first.groupRef))
	b.WriteString(`/region" data-pencode-region><button class="codex-activity-summary" type="button" data-ref="`)
	b.WriteString(html.EscapeString(first.groupRef))
	b.WriteString(`" aria-expanded="`)
	b.WriteString(strconv.FormatBool(first.groupExpanded))
	b.WriteString(`"><img src="resources/icons/images.svg" alt=""><span>已查看 `)
	writeActivityCount(b, len(items))
	b.WriteString(` 张图像</span><img class="codex-activity-chevron" src="resources/icons/chevron-down.svg" alt=""></button>`)
	bindings = append(bindings, peb.EditorBinding{Ref: first.groupRef, Kind: "click"})
	if first.groupExpanded {
		b.WriteString(`<div class="codex-images" aria-label="已查看的图像">`)
		for _, i := range items {
			if i.html == "" {
				var thumbnail strings.Builder
				i.renderViewedImage(&thumbnail, root)
				i.html = thumbnail.String()
			}
			b.WriteString(i.html)
			bindings = append(bindings, i.linkBindings...)
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</section>`)
	return bindings
}

func (i *chatItem) renderViewedImage(b *strings.Builder, root string) {
	if len(i.images) == 0 {
		preview, _ := localImagePreview(i.data.Path, root, 160)
		i.images = append(i.images, imageAttachment{preview: preview})
	}
	ref := "view-image/" + i.data.ID
	i.linkBindings = append(i.linkBindings[:0], peb.EditorBinding{Ref: ref, Kind: "click"})
	b.WriteString(`<button type="button" class="codex-viewed-image" data-ref="`)
	b.WriteString(html.EscapeString(ref))
	b.WriteString(`" aria-haspopup="dialog" aria-label="查看图片 `)
	b.WriteString(html.EscapeString(filepath.Base(i.data.Path)))
	b.WriteString(`" title="`)
	b.WriteString(html.EscapeString(i.data.Path))
	b.WriteString(`">`)
	if i.images[0].preview == "" {
		b.WriteString(`<span class="codex-muted">图片不可用</span>`)
	} else {
		b.WriteString(`<img src="`)
		b.WriteString(i.images[0].preview)
		b.WriteString(`" alt="`)
		b.WriteString(html.EscapeString(filepath.Base(i.data.Path)))
		b.WriteString(`">`)
	}
	b.WriteString(`</button>`)
}
