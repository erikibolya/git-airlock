//go:build linux

package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func installUpdate(o UpdateOptions) error {
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	unitDir := filepath.Join(dir, "systemd", "user")
	execStart := fmt.Sprintf("%q self-update --manifest %q", o.ExePath, o.ManifestURL)
	for _, s := range o.RestartServices {
		if s != "" {
			execStart += fmt.Sprintf(" --restart-service %q", s)
		}
	}
	service := fmt.Sprintf(`[Unit]
Description=Git Airlock self-update
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=%s
`, execStart)
	timer := `[Unit]
Description=Daily Git Airlock update check

[Timer]
OnCalendar=daily
Persistent=true
RandomizedDelaySec=1h

[Install]
WantedBy=timers.target
`
	servicePath := filepath.Join(unitDir, "git-airlock-update.service")
	timerPath := filepath.Join(unitDir, "git-airlock-update.timer")

	if o.Print {
		fmt.Printf("Write %s:\n%s\nWrite %s:\n%s\n", servicePath, service, timerPath, timer)
		fmt.Println("Run: systemctl --user daemon-reload && systemctl --user enable --now git-airlock-update.timer")
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
		{"--user", "enable", "--now", "git-airlock-update.timer"},
	} {
		cmd := exec.Command("systemctl", args...)
		cmd.Env = os.Environ()
		if os.Getenv("XDG_RUNTIME_DIR") == "" {
			cmd.Env = append(cmd.Env, fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", os.Getuid()))
		}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("systemctl %v: %w", args, err)
		}
	}
	fmt.Println("installed systemd user timer git-airlock-update.timer (daily)")
	return nil
}
