//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func hideWindow(*exec.Cmd) {}

func detachServer(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
