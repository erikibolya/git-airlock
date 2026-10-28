package queue

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func newBare(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "mirror.git")
	if out, err := exec.Command("git", "init", "--bare", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	return repo
}

func TestGitStore(t *testing.T) {
	g := NewGit(newBare(t))
	if err := g.Ensure(); err != nil {
		t.Fatal(err)
	}
	id, err := g.Enqueue(&Request{Action: ActionOpen, Head: "feature", Base: "main", Merge: "when-green"})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := g.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != id || pending[0].Head != "feature" {
		t.Fatalf("unexpected pending: %+v", pending)
	}
	if err := g.WriteResult(&Result{ID: id, Action: ActionOpen, OK: true, Message: "done", PR: 12}); err != nil {
		t.Fatal(err)
	}
	res, err := g.ReadResult(id)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.PR != 12 || res.Message != "done" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if err := g.MarkProcessed(id); err != nil {
		t.Fatal(err)
	}
	pending, err = g.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("request still pending: %+v", pending)
	}
	if g.Location(id) == "" {
		t.Fatal("empty location")
	}
}

func TestGitStoreWait(t *testing.T) {
	g := NewGit(newBare(t))
	id, err := g.Enqueue(&Request{Action: ActionStatus, Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = g.WriteResult(&Result{ID: id, Action: ActionStatus, Pending: true, Message: "waiting"})
		time.Sleep(20 * time.Millisecond)
		_ = g.WriteResult(&Result{ID: id, Action: ActionStatus, OK: true, Message: "done"})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, err := g.Wait(ctx, id, 10*time.Millisecond, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Message != "done" {
		t.Fatalf("unexpected: %+v", res)
	}
}

func TestGitStoreMissingResult(t *testing.T) {
	g := NewGit(newBare(t))
	if _, err := g.ReadResult("nope"); err == nil {
		t.Fatal("expected an error")
	}
}
