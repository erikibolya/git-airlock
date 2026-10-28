//go:build !darwin

package install

import "fmt"

func installDarwin(o Options) error {
	return fmt.Errorf("install-task: launchd is not available on this OS")
}

func statusDarwin() (string, error) { return "unsupported", nil }
