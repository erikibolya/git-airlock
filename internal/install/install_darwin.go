//go:build darwin

package install

import (
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func installDarwin(o Options) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	label := "com.gitairlock." + slug(o.Name)
	plistPath := filepath.Join(dir, label+".plist")
	seconds := int(o.Interval.Seconds())
	if seconds < 60 {
		seconds = 60
	}
	args := fmt.Sprintf("    <string>%s</string>\n    <string>worker</string>\n", html.EscapeString(o.ExePath))
	for _, p := range o.ConfigPaths {
		args += fmt.Sprintf("    <string>--config</string>\n    <string>%s</string>\n", html.EscapeString(p))
	}
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
%s  </array>
  <key>StartInterval</key><integer>%d</integer>
  <key>RunAtLoad</key><true/>
</dict>
</plist>
`, label, args, seconds)

	if o.Print {
		fmt.Printf("Write %s:\n%s\nRun: launchctl load -w %s\n", plistPath, plist, plistPath)
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return err
	}
	cmd := exec.Command("launchctl", "load", "-w", plistPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launchctl: %w", err)
	}
	fmt.Printf("installed launchd agent %s (every %s)\n", label, o.Interval)
	return nil
}

func statusDarwin() (string, error) {
	out, err := exec.Command("launchctl", "list").Output()
	if err != nil {
		return "unknown", nil
	}
	if strings.Contains(string(out), "com.gitairlock.") {
		return "installed", nil
	}
	return "not installed", nil
}
