//go:build !linux

package install

import "fmt"

func installUpdate(o UpdateOptions) error {
	return fmt.Errorf("install-update-task is only implemented on Linux/WSL")
}
