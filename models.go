package main

import (
	"fmt"
	"html"
	"strings"

	"github.com/pencode-labs/pencode/peb"
)

type modelOption struct {
	Model                     string                  `json:"model"`
	DisplayName               string                  `json:"displayName"`
	Hidden                    bool                    `json:"hidden"`
	IsDefault                 bool                    `json:"isDefault"`
	DefaultReasoningEffort    string                  `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []reasoningEffortOption `json:"supportedReasoningEfforts"`
}

type reasoningEffortOption struct {
	ReasoningEffort string `json:"reasoningEffort"`
}

func effortLabel(effort string) string {
	switch effort {
	case "none":
		return "无"
	case "minimal":
		return "最低"
	case "low":
		return "低"
	case "medium":
		return "中"
	case "high":
		return "高"
	case "xhigh":
		return "超高"
	case "max":
		return "最高"
	case "ultra":
		return "极高"
	}
	return effort
}

func (m *modelOption) label() string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	return m.Model
}

func (a *agent) findModel(id string) *modelOption {
	for i := range a.models {
		if a.models[i].Model == id {
			return &a.models[i]
		}
	}
	return nil
}

func (a *agent) turnEffort() string {
	if a.effort != "" {
		return a.effort
	}
	for i := range a.models {
		m := &a.models[i]
		if m.Model == a.model || a.model == "" && m.IsDefault {
			return m.DefaultReasoningEffort
		}
	}
	return ""
}

func (a *agent) modelLabel() string {
	if a.model == "" {
		return "默认"
	}
	if m := a.findModel(a.model); m != nil {
		return m.label()
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
	if err := a.closePermissions(); err != nil {
		return err
	}
	a.modelSettings, a.composerMenu, a.panelDirty = true, false, true
	a.modelError = ""
	a.pendingModel = ""
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
	a.modelSettings, a.pendingModel, a.panelDirty = false, "", true
	return a.publisher.CloseUI("models")
}

func (a *agent) handleModelEvent(e peb.EditorEvent) error {
	if e.Kind == "close" {
		a.modelSettings, a.pendingModel, a.panelDirty = false, "", true
		return nil
	}
	if e.Kind != "click" || !a.modelSettings {
		return nil
	}
	if e.Ref == "retry-models" {
		return a.openModels()
	}
	if a.pendingModel != "" {
		if e.Ref == "models-back" {
			a.pendingModel = ""
			return a.showModels()
		}
		if effort, ok := strings.CutPrefix(e.Ref, "effort/"); ok {
			if m := a.findModel(a.pendingModel); m != nil {
				for _, option := range m.SupportedReasoningEfforts {
					if effort != "" && option.ReasoningEffort == effort {
						if err := a.saveProjectSettings(m.Model, effort, a.permissionIndex); err != nil {
							return err
						}
						return a.closeModels()
					}
				}
			}
		}
		return nil
	}
	if e.Ref == "model-default" {
		if err := a.saveProjectSettings("", "", a.permissionIndex); err != nil {
			return err
		}
		return a.closeModels()
	}
	if id, ok := strings.CutPrefix(e.Ref, "model/"); ok {
		if m := a.findModel(id); m != nil {
			if len(m.SupportedReasoningEfforts) != 0 {
				a.pendingModel = id
				return a.showModels()
			}
			if err := a.saveProjectSettings(id, "", a.permissionIndex); err != nil {
				return err
			}
			return a.closeModels()
		}
	}
	return nil
}

func (a *agent) showModels() error {
	title := "选择模型"
	if a.pendingModel != "" {
		title = "选择推理强度"
	}
	return a.publisher.Show(peb.UI{
		ID: "models", Title: title, Kind: peb.ContextMenu,
		Anchor: "ui:composer#model-settings", Context: a.context,
		Placement: peb.UIPlacement{Mode: peb.PositionAnchorAbove, Y: -6, Width: 256},
		View:      a.modelsView(),
	})
}

func (a *agent) modelsView() *peb.EditorView {
	var b strings.Builder
	b.WriteString(`<section class="codex-model-menu" role="menu" aria-label="模型与推理强度">`)
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
	if m := a.findModel(a.pendingModel); a.pendingModel != "" && m != nil {
		b.WriteString(`<button type="button" role="menuitem" data-ref="models-back">返回模型列表</button><h3>选择推理强度</h3><p class="codex-model-group">`)
		b.WriteString(html.EscapeString(m.label()))
		b.WriteString(`</p>`)
		bindings = append(bindings, peb.EditorBinding{Ref: "models-back", Kind: "click"})
		selected := m.DefaultReasoningEffort
		if m.Model == a.model && a.effort != "" {
			selected = a.effort
		}
		for _, option := range m.SupportedReasoningEfforts {
			if option.ReasoningEffort != "" {
				row("effort/"+option.ReasoningEffort, effortLabel(option.ReasoningEffort), selected == option.ReasoningEffort)
			}
		}
		b.WriteString(`</section>`)
		return &peb.EditorView{HTML: b.String(), CSS: chatCSS, Events: bindings}
	}
	b.WriteString(`<h3>选择模型</h3>`)
	row("model-default", "默认", a.model == "")
	b.WriteString(`<p class="codex-model-group">推荐模型集</p>`)
	for _, m := range a.models {
		row("model/"+m.Model, m.label(), a.model == m.Model)
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
