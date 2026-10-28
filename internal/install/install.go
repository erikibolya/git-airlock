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
	// ConfigPath is the absolute .airlock.toml path.
	ConfigPath string
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
	if o.ConfigPath == "" {
		return fmt.Errorf("config path is required")
	}
	if abs, err := filepath.Abs(o.ConfigPath); err == nil {
		o.ConfigPath = abs
	}
	return nil
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
