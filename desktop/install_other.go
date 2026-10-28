//go:build !windows

package main

import (
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/erikibolya/git-airlock/internal/update"
)

// installUpdate on Linux/macOS points at the release page; package managers
// handle the actual upgrade.
func (a *App) installUpdate(m *update.Manifest) {
	a.info("Update available", "Download the new version from the release page. Homebrew and Scoop users can upgrade with the package manager.")
	if a.ctx != nil {
		wailsruntime.BrowserOpenURL(a.ctx, releasePageURL)
	}
}
