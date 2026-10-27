package main

import (
	"strings"

	"github.com/pencode-labs/pencode/peb"
)

type approvalSettings struct {
	ApprovalPolicy    string `json:"approvalPolicy"`
	ApprovalsReviewer string `json:"approvalsReviewer"`
}

type sandboxPolicy struct {
	Type          string `json:"type"`
	NetworkAccess bool   `json:"networkAccess,omitempty"`
}

type permissionOption struct {
	ref, label, description, icon, sandbox string
	approvalSettings
	policy sandboxPolicy
}

var permissionOptions = [...]permissionOption{
	{
		ref: "permission/ask", label: "请批准", description: "编辑外部文件和使用互联网时始终询问", icon: "hand",
		sandbox: "workspace-write", approvalSettings: approvalSettings{"on-request", "user"}, policy: sandboxPolicy{Type: "workspaceWrite"},
	},
	{
		ref: "permission/auto", label: "帮我批准", description: "仅对检测到的风险操作请求批准", icon: "shield-check",
		sandbox: "workspace-write", approvalSettings: approvalSettings{"on-request", "auto_review"}, policy: sandboxPolicy{Type: "workspaceWrite"},
	},
	{
		ref: "permission/full", label: "完全访问权限", description: "可不受限制地访问互联网和电脑上的任何文件", icon: "shield-alert",
		sandbox: "danger-full-access", approvalSettings: approvalSettings{"never", "user"}, policy: sandboxPolicy{Type: "dangerFullAccess"},
	},
}

func (a *agent) permission() *permissionOption {
	return &permissionOptions[a.permissionIndex]
}

func (a *agent) openPermissions() error {
	if err := a.closeModels(); err != nil {
		return err
	}
	a.permissionSettings, a.composerMenu, a.panelDirty = true, false, true
	a.permissionHelp = false
	return a.showPermissions()
}

func (a *agent) closePermissions() error {
	if !a.permissionSettings {
		return nil
	}
	a.permissionSettings, a.permissionHelp, a.panelDirty = false, false, true
	return a.publisher.CloseUI("permissions")
}

func (a *agent) closeComposerMenus() error {
	if err := a.closeModels(); err != nil {
		return err
	}
	return a.closePermissions()
}

func (a *agent) handlePermissionEvent(e peb.EditorEvent) error {
	if e.Kind == "close" {
		a.permissionSettings, a.permissionHelp, a.panelDirty = false, false, true
		return nil
	}
	if e.Kind != "click" || !a.permissionSettings {
		return nil
	}
	if e.Ref == "permission-help" {
		a.permissionHelp = !a.permissionHelp
		return a.showPermissions()
	}
	for i := range permissionOptions {
		if e.Ref == permissionOptions[i].ref {
			if err := a.saveProjectSettings(a.model, a.effort, i); err != nil {
				return err
			}
			return a.closePermissions()
		}
	}
	return nil
}

func (a *agent) showPermissions() error {
	return a.publisher.Show(peb.UI{
		ID: "permissions", Title: "权限设置", Kind: peb.ContextMenu,
		Anchor: "ui:composer#permission-settings", Context: a.context,
		Placement: peb.UIPlacement{Mode: peb.PositionAnchorAbove, Y: -6, Width: 336},
		View:      a.permissionsView(),
	})
}

func (a *agent) permissionsView() *peb.EditorView {
	var b strings.Builder
	b.WriteString(`<section class="codex-permission-menu" role="menu" aria-label="权限设置"><div class="codex-permission-header"><h3>应如何批准 Codex 操作？</h3><button data-ref="permission-help" class="codex-permission-help" type="button" aria-expanded="`)
	if a.permissionHelp {
		b.WriteString(`true">收起说明`)
	} else {
		b.WriteString(`false">了解更多`)
	}
	b.WriteString(`</button></div>`)
	bindings := make([]peb.EditorBinding, 0, len(permissionOptions)+1)
	bindings = append(bindings, peb.EditorBinding{Ref: "permission-help", Kind: "click"})
	for i := range permissionOptions {
		p := &permissionOptions[i]
		b.WriteString(`<button class="codex-permission-option`)
		if p.sandbox == "danger-full-access" {
			b.WriteString(` codex-permission-full`)
		}
		b.WriteString(`" type="button" role="menuitemradio" data-ref="`)
		b.WriteString(p.ref)
		b.WriteString(`" aria-checked="`)
		if i == a.permissionIndex {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
		b.WriteString(`"><img class="codex-permission-icon" src="resources/icons/`)
		b.WriteString(p.icon)
		b.WriteString(`.svg" alt=""><span class="codex-permission-copy"><span class="codex-permission-label">`)
		b.WriteString(p.label)
		b.WriteString(`</span><span class="codex-permission-description">`)
		b.WriteString(p.description)
		b.WriteString(`</span></span>`)
		if i == a.permissionIndex {
			b.WriteString(`<img class="codex-permission-check" src="resources/icons/`)
			if p.sandbox == "danger-full-access" {
				b.WriteString("check-warning")
			} else {
				b.WriteString("check")
			}
			b.WriteString(`.svg" alt="">`)
		}
		b.WriteString(`</button>`)
		bindings = append(bindings, peb.EditorBinding{Ref: p.ref, Kind: "click"})
	}
	if a.permissionHelp {
		b.WriteString(`<p class="codex-permission-explanation">前两档允许在工作区内修改文件和执行命令；外部文件和网络访问分别交由你或 Codex 自动审查。自动审查仍可能拒绝风险操作，并会产生额外用量。完全访问允许直接执行操作。设置从下一次发送开始生效，不影响正在运行的任务；远程项目的文件和命令权限作用于工作区服务器。</p>`)
	}
	b.WriteString(`</section>`)
	return &peb.EditorView{HTML: b.String(), CSS: chatCSS, Events: bindings}
}
