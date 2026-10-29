package install

import (
	"os"
	"path/filepath"
)

// workerCommand returns the executable and arguments for the scheduled task.
// The installer ships airlock-worker.exe, a GUI-subsystem build of the same
// code: Task Scheduler can start it without creating a console window. Older
// or portable installs without that file fall back to airlock.exe, which hides
// its own console.
func workerCommand(exePath string) (string, string) {
	worker := filepath.Join(filepath.Dir(exePath), "airlock-worker.exe")
	if _, err := os.Stat(worker); err == nil {
		return worker, "worker"
	}
	return exePath, "worker --hidden"
}
