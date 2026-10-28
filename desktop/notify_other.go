//go:build !windows

package main

import (
	"os/exec"
	goruntime "runtime"
	"strings"
)

// notify uses the desktop notification helper available on the platform.
func notify(title, body string) {
	if goruntime.GOOS == "darwin" {
		script := `display notification "` + strings.ReplaceAll(body, `"`, `\"`) +
			`" with title "` + strings.ReplaceAll(title, `"`, `\"`) + `"`
		_ = exec.Command("osascript", "-e", script).Run()
		return
	}
	_ = exec.Command("notify-send", title, body).Run()
}
