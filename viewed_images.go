package main

import (
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/onix-engine/onix/html/markup"
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

func renderViewedImages(b *markup.Builder, bindings []peb.EditorBinding, items []*chatItem, root string) []peb.EditorBinding {
	first := items[0]
	if first.groupRef == "" {
		first.groupRef = "activity/" + first.data.ID
	}
	b.Open("section", "class", "codex-image-views", "data-ref", first.groupRef+"/region", "data-pencode-region", "")
	b.Open("button", "class", "codex-activity-summary", "type", "button", "data-ref", first.groupRef, "aria-expanded", strconv.FormatBool(first.groupExpanded))
	b.Element("img", "src", "resources/icons/images.svg", "alt", "")
	b.Open("span")
	b.Text("已查看 ")
	b.Text(strconv.Itoa(len(items)))
	b.Text(" 张图像")
	b.End()
	b.Element("img", "class", "codex-activity-chevron", "src", "resources/icons/chevron-down.svg", "alt", "")
	b.End()
	bindings = append(bindings, peb.EditorBinding{Ref: first.groupRef, Kind: "click"})
	if first.groupExpanded {
		b.Open("div", "class", "codex-images", "aria-label", "已查看的图像")
		for _, i := range items {
			if i.html == "" {
				var thumbnail markup.Builder
				i.renderViewedImage(&thumbnail, root)
				i.html = thumbnail.String()
			}
			b.Embed(i.html)
			bindings = append(bindings, i.linkBindings...)
		}
		b.End()
	}
	b.End()
	return bindings
}

func (i *chatItem) renderViewedImage(b *markup.Builder, root string) {
	if len(i.images) == 0 {
		preview, _ := localImagePreview(i.data.Path, root, 160)
		i.images = append(i.images, imageAttachment{preview: preview})
	}
	ref := "view-image/" + i.data.ID
	i.linkBindings = append(i.linkBindings[:0], peb.EditorBinding{Ref: ref, Kind: "click"})
	b.Open("button", "type", "button", "class", "codex-viewed-image", "data-ref", ref, "aria-haspopup", "dialog", "aria-label", "查看图片 "+filepath.Base(i.data.Path), "title", i.data.Path)
	if i.images[0].preview == "" {
		b.TextElement("span", "图片不可用", "class", "codex-muted")
	} else {
		b.Element("img", "src", i.images[0].preview, "alt", filepath.Base(i.data.Path))
	}
	b.End()
}
