package main

import (
	"strconv"
	"strings"

	"github.com/onix-engine/onix/html/markup"
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
	ref, label, title, icon, sandbox string
	approvalSettings
	policy sandboxPolicy
}

const permissionTiming = "；从下一轮任务开始生效"

var permissionOptions = [...]permissionOption{
	{
		ref: "permission/ask", label: "请批准", title: "编辑外部文件和使用互联网时始终询问" + permissionTiming, icon: "resources/icons/hand.svg",
		sandbox: "workspace-write", approvalSettings: approvalSettings{"on-request", "user"}, policy: sandboxPolicy{Type: "workspaceWrite"},
	},
	{
		ref: "permission/auto", label: "帮我批准", title: "仅对检测到的风险操作请求批准" + permissionTiming, icon: "resources/icons/shield-check.svg",
		sandbox: "workspace-write", approvalSettings: approvalSettings{"on-request", "auto_review"}, policy: sandboxPolicy{Type: "workspaceWrite"},
	},
	{
		ref: "permission/full", label: "完全访问权限", title: "可不受限制地访问互联网和电脑上的任何文件" + permissionTiming, icon: "resources/icons/shield-alert.svg",
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
	a.permissionSettings, a.workspaceDirty = true, true
	a.permissionHelp = false
	return a.showPermissions()
}

func (a *agent) closePermissions() error {
	if !a.permissionSettings {
		return nil
	}
	a.permissionSettings, a.permissionHelp, a.workspaceDirty = false, false, true
	return a.publisher.CloseUI("permissions")
}

func (a *agent) closeComposerMenus() error {
	a.dismissCompletion()
	if err := a.closeModels(); err != nil {
		return err
	}
	if err := a.closePermissions(); err != nil {
		return err
	}
	if err := a.closeFiles(); err != nil {
		return err
	}
	return a.closeRename()
}

func (a *agent) handlePermissionEvent(e peb.EditorEvent) error {
	if e.Kind == "close" {
		a.permissionSettings, a.permissionHelp, a.workspaceDirty = false, false, true
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
	b := markup.Acquire()
	defer b.Release()
	b.Open("section", "class", "codex-permission-menu", "role", "menu", "aria-label", "权限设置")
	b.Open("div", "class", "codex-permission-header")
	b.TextElement("h3", "应如何批准 Codex 操作？")
	label := "了解更多"
	if a.permissionHelp {
		label = "收起说明"
	}
	b.TextElement("button", label, "data-ref", "permission-help", "class", "codex-permission-help", "type", "button", "aria-expanded", strconv.FormatBool(a.permissionHelp))
	b.End()
	bindings := make([]peb.EditorBinding, 0, len(permissionOptions)+1)
	bindings = append(bindings, peb.EditorBinding{Ref: "permission-help", Kind: "click"})
	for i := range permissionOptions {
		p := &permissionOptions[i]
		class, check := "codex-permission-option", "check"
		if p.sandbox == "danger-full-access" {
			class += " codex-permission-full"
			check = "check-warning"
		}
		b.Open("button", "class", class, "type", "button", "role", "menuitemradio", "data-ref", p.ref, "aria-checked", strconv.FormatBool(i == a.permissionIndex))
		b.Element("img", "class", "codex-permission-icon", "src", p.icon, "alt", "")
		b.Open("span", "class", "codex-permission-copy")
		b.TextElement("span", p.label, "class", "codex-permission-label")
		b.TextElement("span", strings.TrimSuffix(p.title, permissionTiming), "class", "codex-permission-description")
		b.End()
		if i == a.permissionIndex {
			b.Element("img", "class", "codex-permission-check", "src", "resources/icons/"+check+".svg", "alt", "")
		}
		b.End()
		bindings = append(bindings, peb.EditorBinding{Ref: p.ref, Kind: "click"})
	}
	if a.permissionHelp {
		b.TextElement("p", "前两档允许在工作区内修改文件和执行命令；外部文件和网络访问分别交由你或 Codex 自动审查。自动审查仍可能拒绝风险操作，并会产生额外用量。完全访问允许直接执行操作。设置从下一轮任务开始生效，不影响正在运行的任务；远程项目的文件和命令权限作用于工作区服务器。", "class", "codex-permission-explanation")
	}
	b.End()
	return &peb.EditorView{HTML: b.String(), CSS: chatCSS, Events: bindings}
}
