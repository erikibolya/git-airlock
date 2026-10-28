package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/erikibolya/git-airlock/internal/update"
)

// version is set with -ldflags "-X main.version=..." by the release build.
var version = "dev"

const (
	updateManifestURL = "https://github.com/erikibolya/git-airlock/releases/latest/download/latest.json"
	updatePublicKey   = "Acgkri/7VClnySjUZCHXEKZducmObsLKc6mgcfgiJWQ="
	releasePageURL    = "https://github.com/erikibolya/git-airlock/releases/latest"
)

func manifestURL() string {
	if u := os.Getenv("AIRLOCK_UPDATE_URL"); u != "" {
		return u
	}
	return updateManifestURL
}

// startUpdateCheck runs the automatic startup check after the window is ready.
func (a *App) startUpdateCheck() {
	if version == "dev" {
		return
	}
	go func() {
		time.Sleep(5 * time.Second)
		a.checkUpdates(false)
	}()
}

func (a *App) checkUpdates(interactive bool) {
	if version == "dev" {
		if interactive {
			a.info("Git Airlock", "Update checks are disabled in development builds.")
		}
		return
	}
	m, err := update.Fetch(context.Background(), manifestURL())
	if err != nil {
		log.Printf("update check: %v", err)
		if interactive {
			a.error("Update check failed", err.Error())
		}
		return
	}
	pub, err := update.ParsePublicKey(updatePublicKey)
	if err != nil {
		if interactive {
			a.error("Update check failed", err.Error())
		}
		return
	}
	if err := update.Verify(pub, m.Version, m.Windows.SHA256, m.Windows.Signature); err != nil {
		if interactive {
			a.error("Update check failed", "The update manifest signature is invalid.\n\n"+err.Error())
		}
		return
	}
	if update.Compare(m.Version, version) <= 0 {
		if interactive {
			a.info("Git Airlock", "You have the latest version ("+update.NormalizeVersion(version)+").")
		}
		return
	}
	notes := m.Notes
	if notes == "" {
		notes = "See the release page for details."
	}
	log.Printf("update check: current=%s latest=%s", update.NormalizeVersion(version), update.NormalizeVersion(m.Version))
	// Windows MessageBox only offers Yes/No; macOS/Linux use the labels.
	choice, err := wailsruntime.MessageDialog(a.ctx, wailsruntime.MessageDialogOptions{
		Type:          wailsruntime.QuestionDialog,
		Title:         "Git Airlock update",
		Message:       fmt.Sprintf("Version %s is available (you have %s). Install it now?\n\n%s", update.NormalizeVersion(m.Version), update.NormalizeVersion(version), notes),
		Buttons:       []string{"Install now", "Later"},
		DefaultButton: "Install now",
		CancelButton:  "Later",
	})
	log.Printf("update dialog: choice=%q err=%v", choice, err)
	if err != nil {
		return
	}
	if choice == "Yes" || choice == "Install now" {
		a.installUpdate(m)
	}
}

func (a *App) info(title, message string) {
	if a.ctx == nil {
		return
	}
	_, _ = wailsruntime.MessageDialog(a.ctx, wailsruntime.MessageDialogOptions{
		Type:    wailsruntime.InfoDialog,
		Title:   title,
		Message: message,
	})
}

func (a *App) error(title, message string) {
	if a.ctx == nil {
		return
	}
	_, _ = wailsruntime.MessageDialog(a.ctx, wailsruntime.MessageDialogOptions{
		Type:    wailsruntime.ErrorDialog,
		Title:   title,
		Message: message,
	})
}

// checkUpdatesCLI is used by `airlock-desktop --check-updates` and prints the
// result without starting the GUI.
func checkUpdatesCLI() error {
	url := manifestURL()
	m, err := update.Fetch(context.Background(), url)
	if err != nil {
		return err
	}
	pub, err := update.ParsePublicKey(updatePublicKey)
	if err != nil {
		return err
	}
	if err := update.Verify(pub, m.Version, m.Windows.SHA256, m.Windows.Signature); err != nil {
		return err
	}
	status := "up to date"
	if update.Compare(m.Version, version) > 0 {
		status = "update available"
	}
	text := fmt.Sprintf("current: %s\nlatest:  %s\nsha256:  %s\n%s\n", update.NormalizeVersion(version), update.NormalizeVersion(m.Version), m.Windows.SHA256, status)
	fmt.Print(text)
	_ = os.WriteFile(filepath.Join(os.TempDir(), "git-airlock-update-check.txt"), []byte(text), 0o644)
	return nil
}
