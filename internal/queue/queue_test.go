package queue

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnqueueAndResult(t *testing.T) {
	q := New(t.TempDir())
	id, err := q.Enqueue(&Request{Action: ActionStatus, Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("empty id")
	}
	pending, err := q.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Action != ActionStatus || pending[0].Number != 7 {
		t.Fatalf("unexpected pending: %+v", pending)
	}
	if err := q.WriteResult(&Result{ID: id, Action: ActionStatus, OK: true, Message: "ok", PR: 7}); err != nil {
		t.Fatal(err)
	}
	got, err := q.ReadResult(id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.PR != 7 {
		t.Fatalf("unexpected result: %+v", got)
	}
	if err := q.MarkProcessed(id); err != nil {
		t.Fatal(err)
	}
	pending, _ = q.Pending()
	if len(pending) != 0 {
		t.Fatalf("request not processed: %+v", pending)
	}
}

func TestBrokenRequest(t *testing.T) {
	q := New(t.TempDir())
	if err := q.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(q.RequestsDir(), "bad.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	pending, err := q.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Action != ActionBroken {
		t.Fatalf("unexpected: %+v", pending)
	}
}

func TestWait(t *testing.T) {
	q := New(t.TempDir())
	id, err := q.Enqueue(&Request{Action: ActionStatus, Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = q.WriteResult(&Result{ID: id, Action: ActionStatus, Pending: true, Message: "waiting"})
		time.Sleep(20 * time.Millisecond)
		_ = q.WriteResult(&Result{ID: id, Action: ActionStatus, OK: true, Message: "done"})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var seen int
	res, err := q.Wait(ctx, id, 10*time.Millisecond, func(*Result) { seen++ })
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Message != "done" {
		t.Fatalf("unexpected: %+v", res)
	}
	if seen < 2 {
		t.Fatalf("expected pending and final callbacks, saw %d", seen)
	}
}
