package install

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkerCommand(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "airlock.exe")
	if err := os.WriteFile(exe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd, args := workerCommand(exe)
	if cmd != exe || args != "worker --hidden" {
		t.Fatalf("without worker: got %q %q, want %q %q", cmd, args, exe, "worker --hidden")
	}

	worker := filepath.Join(dir, "airlock-worker.exe")
	if err := os.WriteFile(worker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd, args = workerCommand(exe)
	if cmd != worker || args != "worker" {
		t.Fatalf("with worker: got %q %q, want %q %q", cmd, args, worker, "worker")
	}
}
