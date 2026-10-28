//go:build !windows

package install

import "fmt"

func installWindows(o Options) error {
	return fmt.Errorf("install-task: windows scheduler is not available on this OS")
}

func statusWindows(name string) (string, error) { return "unsupported", nil }
