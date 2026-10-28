//go:build !linux

package install

import "fmt"

func installLinux(o Options) error {
	return fmt.Errorf("install-task: systemd is not available on this OS")
}

func statusLinux() (string, error) { return "unsupported", nil }
