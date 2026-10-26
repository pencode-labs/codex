package main

import (
	"fmt"
	"html"
	"strings"

	"github.com/pencode-labs/pencode/peb"
)

type modelOption struct {
	Model       string `json:"model"`
	DisplayName string `json:"displayName"`
	Hidden      bool   `json:"hidden"`
}

func (a *agent) modelLabel() string {
	if a.model == "" {
		return "默认"
	}
	for _, m := range a.models {
		if m.Model == a.model && m.DisplayName != "" {
			return m.DisplayName
		}
	}
	return a.model
}

func (a *agent) loadModels() error {
	if err := a.connect(); err != nil {
		return err
	}
	if a.modelsLoaded {
		return nil
	}
	var models []modelOption
	cursor := ""
	for {
		page, err := call[struct {
			Data       []modelOption `json:"data"`
			NextCursor string        `json:"nextCursor"`
		}](a.client, "model/list", struct {
			Cursor        string `json:"cursor,omitempty"`
			Limit         int    `json:"limit"`
			IncludeHidden bool   `json:"includeHidden"`
		}{Cursor: cursor, Limit: 100})
		if err != nil {
			return err
		}
		for _, m := range page.Data {
			if !m.Hidden && m.Model != "" {
				models = append(models, m)
			}
		}
		if page.NextCursor == "" {
			break
		}
		if page.NextCursor == cursor {
			return fmt.Errorf("Codex 返回了重复的模型分页游标")
		}
		cursor = page.NextCursor
	}
	a.models, a.modelsLoaded = models, true
	return nil
}

func (a *agent) openModels() error {
	a.modelSettings, a.composerMenu, a.panelDirty = true, false, true
	a.modelError = ""
	if !a.modelsLoaded {
		if err := a.showModels(); err != nil {
			return err
		}
	}
	if err := a.loadModels(); err != nil {
		a.modelError = err.Error()
	}
	return a.showModels()
}

func (a *agent) closeModels() error {
	if !a.modelSettings {
		return nil
	}
	a.modelSettings, a.panelDirty = false, true
	return a.publisher.CloseUI("models")
}

func (a *agent) handleModelEvent(e peb.EditorEvent) error {
	if e.Kind == "close" {
		a.modelSettings, a.panelDirty = false, true
		return nil
	}
	if e.Kind != "click" || !a.modelSettings {
		return nil
	}
	if e.Ref == "retry-models" {
		return a.openModels()
	}
	if e.Ref == "model-default" {
		a.model = ""
		return a.closeModels()
	}
	if id, ok := strings.CutPrefix(e.Ref, "model/"); ok {
		for _, m := range a.models {
			if m.Model == id {
				a.model = id
				return a.closeModels()
			}
		}
	}
	return nil
}

func (a *agent) showModels() error {
	return a.publisher.Show(peb.UI{
		ID: "models", Title: "选择模型", Kind: peb.ContextMenu,
		Anchor: "ui:composer#model-settings", Context: a.context,
		Placement: peb.UIPlacement{Mode: peb.PositionAnchorAbove, Y: -6, Width: 256},
		View:      a.modelsView(),
	})
}

func (a *agent) modelsView() *peb.EditorView {
	var b strings.Builder
	b.WriteString(`<section class="codex-model-menu"><h3>选择模型</h3>`)
	bindings := make([]peb.EditorBinding, 0, len(a.models)+2)
	row := func(ref, label string, selected bool) {
		b.WriteString(`<button type="button" role="menuitemradio" data-ref="`)
		b.WriteString(html.EscapeString(ref))
		b.WriteString(`" aria-checked="`)
		if selected {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
		b.WriteString(`"><span>`)
		b.WriteString(html.EscapeString(label))
		b.WriteString(`</span>`)
		if selected {
			b.WriteString(`<img src="resources/icons/check.svg" alt="">`)
		}
		b.WriteString(`</button>`)
		bindings = append(bindings, peb.EditorBinding{Ref: ref, Kind: "click"})
	}
	row("model-default", "默认", a.model == "")
	b.WriteString(`<p class="codex-model-group">推荐模型集</p>`)
	for _, m := range a.models {
		label := m.DisplayName
		if label == "" {
			label = m.Model
		}
		row("model/"+m.Model, label, a.model == m.Model)
	}
	switch {
	case a.modelError != "":
		b.WriteString(`<p class="codex-model-message" role="status">`)
		b.WriteString(html.EscapeString(a.modelError))
		b.WriteString(`</p><button type="button" data-ref="retry-models">重新加载</button>`)
		bindings = append(bindings, peb.EditorBinding{Ref: "retry-models", Kind: "click"})
	case !a.modelsLoaded:
		b.WriteString(`<p class="codex-model-message" role="status">正在加载模型…</p>`)
	case len(a.models) == 0:
		b.WriteString(`<p class="codex-model-message" role="status">暂无可用模型</p>`)
	}
	b.WriteString(`</section>`)
	return &peb.EditorView{HTML: b.String(), CSS: chatCSS, Events: bindings}
}
