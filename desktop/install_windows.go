//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/getlantern/systray"

	"github.com/erikibolya/git-airlock/internal/update"
)

func (a *App) installUpdate(m *update.Manifest) {
	pub, err := update.ParsePublicKey(updatePublicKey)
	if err != nil {
		a.error("Update failed", err.Error())
		return
	}
	a.showWindow()
	systray.SetTooltip("Git Airlock: downloading update…")
	dest := filepath.Join(os.TempDir(), "GitAirlockSetup-"+update.NormalizeVersion(m.Version)+".exe")
	sha, err := update.Download(context.Background(), m.Windows.URL, dest)
	if err != nil {
		a.error("Update download failed", err.Error())
		return
	}
	if !strings.EqualFold(sha, strings.TrimSpace(m.Windows.SHA256)) {
		a.error("Update failed", "The downloaded installer checksum does not match the signed manifest.")
		return
	}
	if err := update.Verify(pub, m.Version, sha, m.Windows.Signature); err != nil {
		a.error("Update failed", "The downloaded installer signature is invalid.\n\n"+err.Error())
		return
	}
	exe, err := os.Executable()
	if err != nil {
		a.error("Update failed", err.Error())
		return
	}
	script := filepath.Join(os.TempDir(), "git-airlock-update.cmd")
	body := "@echo off\r\n" +
		"\"" + dest + "\" /VERYSILENT /SUPPRESSMSGBOXES /NORESTART\r\n" +
		"start \"\" \"" + exe + "\"\r\n"
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		a.error("Update failed", err.Error())
		return
	}
	systray.SetTooltip("Git Airlock: installing update…")
	cmd := exec.Command("cmd.exe", "/c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		a.error("Update failed", err.Error())
		return
	}
	a.quit()
}
