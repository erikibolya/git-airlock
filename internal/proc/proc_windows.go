//go:build windows

// Package proc creates child processes without flashing console windows when
// the parent is a GUI application (the desktop dashboard).
package proc

import (
	"context"
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd
}

func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd
}
