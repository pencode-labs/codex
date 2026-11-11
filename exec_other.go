//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

const connectionRefused = syscall.ECONNREFUSED

func hideWindow(*exec.Cmd) {}

func detachServer(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func startCodexCommand(cmd *exec.Cmd) error { return cmd.Start() }
