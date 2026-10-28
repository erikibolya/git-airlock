//go:build linux

package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func installLinux(o Options) error {
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	unitDir := filepath.Join(dir, "systemd", "user")
	minutes := int(o.Interval.Minutes())
	if minutes < 1 {
		minutes = 1
	}
	service := fmt.Sprintf(`[Unit]
Description=Git Airlock worker

[Service]
Type=oneshot
ExecStart="%s" worker --config "%s"
`, o.ExePath, o.ConfigPath)
	timer := fmt.Sprintf(`[Unit]
Description=Run Git Airlock every %d minute(s)

[Timer]
OnBootSec=1min
OnUnitActiveSec=%dmin
Persistent=true

[Install]
WantedBy=timers.target
`, minutes, minutes)
	servicePath := filepath.Join(unitDir, "git-airlock.service")
	timerPath := filepath.Join(unitDir, "git-airlock.timer")

	if o.Print {
		fmt.Printf("Write %s:\n%s\nWrite %s:\n%s\n", servicePath, service, timerPath, timer)
		fmt.Println("Run: systemctl --user daemon-reload && systemctl --user enable --now git-airlock.timer")
		return nil
	}
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(servicePath, []byte(service), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(timerPath, []byte(timer), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", "--now", "git-airlock.timer"},
	} {
		cmd := exec.Command("systemctl", args...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("systemctl %v: %w", args, err)
		}
	}
	fmt.Printf("installed systemd user timer git-airlock.timer (every %s)\n", o.Interval)
	return nil
}

func statusLinux() (string, error) {
	out, err := exec.Command("systemctl", "--user", "is-active", "git-airlock.timer").Output()
	if err != nil {
		return "not installed", nil
	}
	return strings.TrimSpace(string(out)), nil
}
