package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"slices"
	"strconv"
	"strings"

	"github.com/pencode-labs/pencode/peb"
)

type imageAttachment struct{ url, preview string }

func imagePreview(source string) (string, error) {
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
	w, h := config.Width, config.Height
	if longest := max(w, h); longest > 160 {
		w, h = max(1, w*160/longest), max(1, h*160/longest)
	}
	thumb := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			thumb.Set(x, y, src.At(x*config.Width/w, y*config.Height/h))
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, thumb); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes()), nil
}

func (a *agent) pasteImage(e peb.EditorEvent) error {
	a.setDraft(e.Value)
	if e.InputType == "error" {
		return fmt.Errorf("%s", e.Data)
	}
	if e.InputType != "image/png" {
		return nil
	}
	preview, err := imagePreview(e.Data)
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
