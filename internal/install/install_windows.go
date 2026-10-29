//go:build windows

package install

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

func installWindows(o Options) error {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return fmt.Errorf("LOCALAPPDATA is not set")
	}
	dir := filepath.Join(local, "git-airlock")
	// airlock-worker.exe has no console window; if it is missing, airlock.exe
	// hides its own console (--hidden).
	workerExe, args := workerCommand(o.ExePath)
	for _, p := range o.ConfigPaths {
		args += fmt.Sprintf(` --config "%s"`, p)
	}

	minutes := int(o.Interval.Minutes())
	if minutes < 1 {
		minutes = 1
	}
	xmlPath := filepath.Join(dir, "worker-task.xml")
	xmlBody := taskXML(minutes, workerExe, args)

	if o.Print {
		fmt.Printf("Write %s:\n%s\n", xmlPath, xmlBody)
		fmt.Printf("Run: schtasks /create /tn %q /xml %q /f\n", o.Name, xmlPath)
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeUTF16(xmlPath, xmlBody); err != nil {
		return err
	}
	// Remove the old launcher; the task no longer points at it.
	_ = os.Remove(filepath.Join(dir, "worker.vbs"))

	cmd := exec.Command("schtasks", "/create", "/tn", o.Name, "/xml", xmlPath, "/f")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// Fall back to a direct /tr invocation. It fails on some Windows
		// builds when the command contains quotes, but it is worth trying.
		return fmt.Errorf("schtasks: %w", err)
	}
	fmt.Printf("installed scheduled task %q (every %d minute(s))\n", o.Name, minutes)
	return nil
}

func writeUTF16(path, body string) error {
	units := utf16.Encode([]rune(body))
	buf := make([]byte, 2+len(units)*2)
	buf[0] = 0xFF
	buf[1] = 0xFE
	for i, u := range units {
		binary.LittleEndian.PutUint16(buf[2+i*2:], u)
	}
	return os.WriteFile(path, buf, 0o644)
}

func statusWindows(name string) (string, error) {
	out, err := exec.Command("schtasks", "/query", "/tn", name, "/fo", "list", "/v").Output()
	if err != nil {
		return "not installed", nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if strings.HasPrefix(line, "Status:") || strings.HasPrefix(line, "Stav:") {
			return strings.TrimSpace(line[strings.Index(line, ":")+1:]), nil
		}
	}
	return "unknown", nil
}
