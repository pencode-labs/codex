package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/pencode-labs/pencode/peb"
)

type projectSettings struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
	Permission      string `json:"permission"`
}

func resolveExecutablePath(root, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = strings.TrimSpace(os.Getenv("PENCODE_CODEX_PATH"))
	}
	if path == "" {
		path = "codex"
	}
	path = filepath.FromSlash(path)
	if path == "~" || strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("解析 Codex 用户目录失败：%w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path[1:], string(filepath.Separator)))
	}
	if !filepath.IsAbs(path) {
		if !strings.ContainsRune(path, filepath.Separator) {
			if runtime.GOOS == "darwin" {
				if _, err := exec.LookPath(path); err != nil {
					if executable := loginShellExecutable(path); executable != "" {
						return executable, nil
					}
				}
			}
			return path, nil
		}
		path = filepath.Join(root, path)
	}
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		name := "codex"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		executable, err := exec.LookPath(filepath.Join(path, name))
		if err != nil {
			return "", fmt.Errorf("Codex 路径 %q 是目录，但未找到可执行文件 %q：%w", path, name, err)
		}
		return executable, nil
	}
	return path, nil
}

const (
	loginShellPathStart = "__PENCODE_PATH_START__"
	loginShellPathEnd   = "__PENCODE_PATH_END__"
)

func loginShellPATH(shell string) string {
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := fmt.Sprintf("printf '%s%%s%s' \"$PATH\"", loginShellPathStart, loginShellPathEnd)
	output, err := exec.CommandContext(ctx, shell, "-ilc", command).Output()
	if err != nil {
		return ""
	}
	_, value, ok := strings.Cut(string(output), loginShellPathStart)
	if !ok {
		return ""
	}
	value, _, ok = strings.Cut(value, loginShellPathEnd)
	if !ok {
		return ""
	}
	return value
}

func loginShellExecutable(name string) string {
	path := loginShellPATH(os.Getenv("SHELL"))
	for _, directory := range filepath.SplitList(path) {
		if directory == "" {
			directory = "."
		}
		candidate := filepath.Join(directory, name)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return candidate
		}
	}
	return ""
}

func (a *agent) setContext(ctx peb.ActionContext) error {
	if a.context.ProjectPath == ctx.ProjectPath {
		a.context = ctx
		return nil
	}
	if err := a.closeComposerMenus(); err != nil {
		return err
	}
	a.completion = composerCompletion{}
	a.context = ctx
	a.history, a.showHistory = threadPage{}, false
	a.status, a.model, a.effort, a.permissionIndex = "", "", "", 0
	a.selectConversation(nil)
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
	a.workspaceDirty = true
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
