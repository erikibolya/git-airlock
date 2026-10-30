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
	devManifestURL    = "https://github.com/erikibolya/git-airlock/releases/download/dev-build/dev.json"
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
	m, err := fetchVerifiedManifest(manifestURL())
	if err != nil {
		log.Printf("update check: %v", err)
		if interactive {
			a.error("Update check failed", err.Error())
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
	a.promptInstall(m, "Git Airlock update", fmt.Sprintf("Version %s is available (you have %s). Install it now?\n\n%s", update.NormalizeVersion(m.Version), update.NormalizeVersion(version), notes))
}

// checkDevUpdates installs the rolling build from the dev branch. It is only
// offered from the menu, never by the automatic startup check.
func (a *App) checkDevUpdates() {
	m, err := fetchVerifiedManifest(devManifestURL)
	if err != nil {
		log.Printf("dev update check: %v", err)
		a.error("Dev build check failed", err.Error())
		return
	}
	if update.NormalizeVersion(m.Version) == update.NormalizeVersion(version) {
		a.info("Git Airlock", "You have the latest dev build ("+update.NormalizeVersion(version)+").")
		return
	}
	notes := m.Notes
	if notes == "" {
		notes = "This is a development build from the dev branch."
	}
	msg := fmt.Sprintf("Dev build %s is available (you have %s). Install it now?\n\n%s\n\nDev builds can contain unfinished changes.", update.NormalizeVersion(m.Version), update.NormalizeVersion(version), notes)
	if update.Compare(m.Version, version) < 0 {
		msg += "\n\nThis is older than the version you are running."
	}
	log.Printf("dev update check: current=%s dev=%s", update.NormalizeVersion(version), update.NormalizeVersion(m.Version))
	a.promptInstall(m, "Git Airlock dev build", msg)
}

// revertToStable reinstalls the latest stable release even when the running
// version is newer (for example a dev build).
func (a *App) revertToStable() {
	m, err := fetchVerifiedManifest(manifestURL())
	if err != nil {
		log.Printf("revert to stable: %v", err)
		a.error("Revert failed", err.Error())
		return
	}
	if update.NormalizeVersion(m.Version) == update.NormalizeVersion(version) {
		a.info("Git Airlock", "You are already on the latest stable version ("+update.NormalizeVersion(version)+").")
		return
	}
	msg := fmt.Sprintf("Revert to the latest stable version %s?\n\nYou are running %s.", update.NormalizeVersion(m.Version), update.NormalizeVersion(version))
	if update.Compare(m.Version, version) < 0 {
		msg += "\n\nThis installs an older version than the one you are running."
	}
	log.Printf("revert to stable: current=%s stable=%s", update.NormalizeVersion(version), update.NormalizeVersion(m.Version))
	a.promptInstall(m, "Revert to stable version", msg)
}

// promptInstall asks for confirmation and hands the verified manifest to the
// installer. Windows MessageBox only offers Yes/No; macOS/Linux use the labels.
func (a *App) promptInstall(m *update.Manifest, title, message string) {
	choice, err := wailsruntime.MessageDialog(a.ctx, wailsruntime.MessageDialogOptions{
		Type:          wailsruntime.QuestionDialog,
		Title:         title,
		Message:       message,
		Buttons:       []string{"Install now", "Later"},
		DefaultButton: "Install now",
		CancelButton:  "Later",
	})
	log.Printf("update dialog %q: choice=%q err=%v", title, choice, err)
	if err != nil {
		return
	}
	if choice == "Yes" || choice == "Install now" {
		a.installUpdate(m)
	}
}

// fetchVerifiedManifest downloads a manifest and checks the signature with the
// key embedded in the app.
func fetchVerifiedManifest(url string) (*update.Manifest, error) {
	m, err := update.Fetch(context.Background(), url)
	if err != nil {
		return nil, err
	}
	pub, err := update.ParsePublicKey(updatePublicKey)
	if err != nil {
		return nil, err
	}
	if err := update.Verify(pub, m.Version, m.Windows.SHA256, m.Windows.Signature); err != nil {
		return nil, fmt.Errorf("the update manifest signature is invalid: %w", err)
	}
	return m, nil
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
	m, err := fetchVerifiedManifest(url)
	if err != nil {
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
