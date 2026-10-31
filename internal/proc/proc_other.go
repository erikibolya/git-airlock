//go:build !windows

// Package proc creates child processes. On Unix it is a thin wrapper around
// os/exec; the Windows variant hides console windows for GUI parents.
package proc

import (
	"context"
	"os/exec"
	"time"
)

func Command(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	// A child that inherited stdout/stderr can otherwise keep CombinedOutput
	// blocked after the context deadline (git push -> git-remote-https).
	cmd.WaitDelay = 10 * time.Second
	return cmd
}
