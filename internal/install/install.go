// Package install registers the periodic worker with the OS scheduler.
package install

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Options controls Install.
type Options struct {
	// Name is the task/unit name, default "Git Airlock".
	Name string
	// ConfigPaths are the .airlock.toml paths this worker should process.
	ConfigPaths []string
	// ExePath is the airlock binary; defaults to the running executable.
	ExePath string
	// Interval between worker runs, default 1 minute.
	Interval time.Duration
	// Print only prints the generated files/commands.
	Print bool
}

func (o *Options) normalize() error {
	if o.Name == "" {
		o.Name = "Git Airlock"
	}
	if o.Interval <= 0 {
		o.Interval = time.Minute
	}
	if o.ExePath == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		o.ExePath = exe
	}
	if abs, err := filepath.Abs(o.ExePath); err == nil {
		o.ExePath = abs
	}
	if len(o.ConfigPaths) == 0 {
		return fmt.Errorf("at least one config path is required")
	}
	for i, p := range o.ConfigPaths {
		if p == "" {
			return fmt.Errorf("config path %d is empty", i+1)
		}
		if abs, err := filepath.Abs(p); err == nil {
			o.ConfigPaths[i] = abs
		}
	}
	return nil
}

// UpdateOptions controls InstallUpdateTask.
type UpdateOptions struct {
	// ExePath is the airlock binary; defaults to the running executable.
	ExePath string
	// ManifestURL is the signed release manifest.
	ManifestURL string
	// RestartServices are systemd user services to restart after an update.
	RestartServices []string
	// Print only prints the generated files/commands.
	Print bool
}

func (o *UpdateOptions) normalize() error {
	if o.ExePath == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		o.ExePath = exe
	}
	if abs, err := filepath.Abs(o.ExePath); err == nil {
		o.ExePath = abs
	}
	if o.ManifestURL == "" {
		return fmt.Errorf("manifest URL is required")
	}
	return nil
}

// InstallUpdateTask registers the daily self-update timer.
func InstallUpdateTask(o UpdateOptions) error {
	if err := o.normalize(); err != nil {
		return err
	}
	return installUpdate(o)
}

// Install registers a periodic worker with the platform scheduler.
func Install(o Options) error {
	if err := o.normalize(); err != nil {
		return err
	}
	switch runtime.GOOS {
	case "windows":
		return installWindows(o)
	case "linux":
		return installLinux(o)
	case "darwin":
		return installDarwin(o)
	default:
		return fmt.Errorf("install-task is not implemented on %s", runtime.GOOS)
	}
}

// Status returns a short scheduler status for doctor.
func Status(name string) (string, error) {
	if name == "" {
		name = "Git Airlock"
	}
	switch runtime.GOOS {
	case "windows":
		return statusWindows(name)
	case "linux":
		return statusLinux()
	case "darwin":
		return statusDarwin()
	default:
		return "unsupported", nil
	}
}

func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "git-airlock"
	}
	return out
}
