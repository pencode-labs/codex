package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pencode-labs/pencode/peb"
)

type projectSettings struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	Permission      string `json:"permission"`
}

func (a *agent) setContext(ctx peb.ActionContext) error {
	if a.context.ProjectPath == ctx.ProjectPath {
		a.context = ctx
		return nil
	}
	if err := a.closeComposerMenus(); err != nil {
		return err
	}
	a.context = ctx
	a.history, a.showHistory, a.composerMenu = threadPage{}, false, false
	a.status, a.model, a.effort, a.permissionIndex = "", "", "", 0
	a.selected = nil
	a.panelDirty = true
	if !filepath.IsAbs(ctx.ProjectPath) {
		return nil
	}
	name := filepath.Join(ctx.ProjectPath, ".pencode", "codex.json")
	data, err := os.ReadFile(name)
	if os.IsNotExist(err) {
		return nil
	}
	var settings projectSettings
	if err == nil {
		err = json.Unmarshal(data, &settings)
	}
	if err != nil {
		return fmt.Errorf("读取 Codex 项目配置失败：%w", err)
	}
	if settings.Permission == "" {
		settings.Permission = "ask"
	}
	for i := range permissionOptions {
		if permissionOptions[i].ref == "permission/"+settings.Permission {
			a.model, a.effort, a.permissionIndex = settings.Model, settings.ReasoningEffort, i
			return nil
		}
	}
	return fmt.Errorf("Codex 项目配置的权限无效：%q", settings.Permission)
}

// Save before applying so a failed write leaves the active selection intact.
func (a *agent) saveProjectSettings(model, effort string, permissionIndex int) error {
	if !filepath.IsAbs(a.context.ProjectPath) {
		return fmt.Errorf("请先打开项目目录，再保存 Codex 设置")
	}
	settings := projectSettings{Model: model, ReasoningEffort: effort, Permission: strings.TrimPrefix(permissionOptions[permissionIndex].ref, "permission/")}
	if err := writeProjectSettings(a.context.ProjectPath, settings); err != nil {
		return fmt.Errorf("保存 Codex 项目配置失败：%w", err)
	}
	a.model, a.effort, a.permissionIndex = model, effort, permissionIndex
	a.panelDirty = true
	return nil
}

func writeProjectSettings(root string, settings projectSettings) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Join(root, ".pencode")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".codex-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(directory, "codex.json"))
}
