package queue

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRemoteStore(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	r1 := newRemote(remote, filepath.Join(t.TempDir(), "c1.git"))
	r2 := newRemote(remote, filepath.Join(t.TempDir(), "c2.git"))

	id, err := r1.Enqueue(&Request{Action: ActionOpen, Head: "feature", Base: "main", Merge: "when-green"})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := r2.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != id || pending[0].Head != "feature" {
		t.Fatalf("unexpected pending: %+v", pending)
	}

	if err := r2.WriteResult(&Result{ID: id, Action: ActionOpen, OK: true, Message: "done", PR: 3}); err != nil {
		t.Fatal(err)
	}
	res, err := r1.ReadResult(id)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.PR != 3 {
		t.Fatalf("unexpected result: %+v", res)
	}

	if err := r2.MarkProcessed(id); err != nil {
		t.Fatal(err)
	}
	pending, err = r1.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("request not deleted: %+v", pending)
	}
}

func TestRemoteStoreNoRequestRefs(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	r := newRemote(remote, filepath.Join(t.TempDir(), "cache.git"))
	pending, err := r.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("unexpected: %+v", pending)
	}
}
