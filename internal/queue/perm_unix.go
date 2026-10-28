//go:build !windows

package queue

import "os"

// relaxRef makes a ref file writable by the other side of the airlock. The
// container runs as root while the Windows/SMB user is uid 1000; Git's default
// 0644 root-owned ref would otherwise be undeletable for the worker.
func relaxRef(path string) {
	_ = os.Chmod(path, 0o666)
	if os.Getuid() == 0 {
		_ = os.Chown(path, 1000, 1000)
	}
}
