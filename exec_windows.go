//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const connectionRefused = syscall.Errno(10061) // WSAECONNREFUSED

func hideWindow(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }

func detachServer(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x00000008 | 0x00000200} // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
}

func tokenInformation[T any](token syscall.Token, class uint32) (value T, err error) {
	var size uint32
	err = syscall.GetTokenInformation(token, class, (*byte)(unsafe.Pointer(&value)), uint32(unsafe.Sizeof(value)), &size)
	return value, err
}

// Shared Codex processes must not inherit an elevated editor's privileges.
func startCodexCommand(cmd *exec.Cmd) error {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return fmt.Errorf("读取 Codex 启动权限：%w", err)
	}
	defer token.Close()
	elevated, err := tokenInformation[uint32](token, syscall.TokenElevation)
	if err != nil {
		return fmt.Errorf("读取 Codex 启动权限：%w", err)
	}
	if elevated != 0 {
		return startCodexWithShellParent(cmd, token)
	}
	return cmd.Start()
}

// A shell parent supplies a real primary token. Linked tokens can be limited
// to identification and cannot be passed directly to CreateProcessAsUser.
// Go duplicates our stdio handles into the parent and closes those copies.
func startCodexWithShellParent(cmd *exec.Cmd, current syscall.Token) error {
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(windows.GetShellWindow(), &pid); err != nil {
		return fmt.Errorf("查找普通桌面进程，请以非管理员方式运行 Pencode：%w", err)
	}
	parent, err := windows.OpenProcess(windows.PROCESS_CREATE_PROCESS|windows.PROCESS_DUP_HANDLE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return fmt.Errorf("打开普通桌面进程：%w", err)
	}
	defer windows.CloseHandle(parent)
	var token windows.Token
	if err := windows.OpenProcessToken(parent, windows.TOKEN_QUERY, &token); err != nil {
		return fmt.Errorf("读取桌面进程权限：%w", err)
	}
	defer token.Close()
	elevated, err := tokenInformation[uint32](syscall.Token(token), syscall.TokenElevation)
	if err != nil {
		return fmt.Errorf("读取桌面进程权限：%w", err)
	}
	if elevated != 0 {
		return fmt.Errorf("桌面进程具有管理员权限，请以非管理员方式运行 Pencode")
	}
	user, err := windows.Token(current).GetTokenUser()
	if err != nil {
		return fmt.Errorf("读取当前用户：%w", err)
	}
	shellUser, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("读取桌面用户：%w", err)
	}
	if !user.User.Sid.Equals(shellUser.User.Sid) {
		return fmt.Errorf("桌面进程属于其他账号，请以桌面用户的非管理员权限运行 Pencode")
	}
	cmd.SysProcAttr.ParentProcess = syscall.Handle(parent)
	// The desktop shell has no console to inherit; proxies use pipes instead.
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
	codexLog.Printf("Codex 子进程继承普通桌面进程权限: parent=%d", pid)
	return cmd.Start()
}
