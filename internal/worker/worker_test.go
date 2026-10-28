package worker

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/erikibolya/git-airlock/internal/config"
	"github.com/erikibolya/git-airlock/internal/provider"
	"github.com/erikibolya/git-airlock/internal/queue"
)

type fakeProvider struct {
	pr          *provider.PR
	checks      provider.Checks
	createCalls int
	mergeCalls  int
}

func (f *fakeProvider) Name() string               { return "fake" }
func (f *fakeProvider) Ping(context.Context) error { return nil }
func (f *fakeProvider) Merge(context.Context, int) error {
	f.mergeCalls++
	if f.pr != nil {
		f.pr.Merged = true
		f.pr.State = "closed"
	}
	return nil
}
func (f *fakeProvider) FindOpenPR(context.Context, string, string) (*provider.PR, error) {
	if f.pr == nil || f.pr.Merged || f.pr.State != "open" {
		return nil, nil
	}
	return f.pr, nil
}
func (f *fakeProvider) FindPR(context.Context, string, string) (*provider.PR, error) {
	return f.pr, nil
}
func (f *fakeProvider) CreatePR(context.Context, provider.CreateRequest) (*provider.PR, error) {
	f.createCalls++
	n := 1
	if f.pr != nil {
		if f.pr.State == "open" {
			return nil, provider.ErrAlreadyExists
		}
		n = f.pr.Number + 1
	}
	f.pr = &provider.PR{Number: n, URL: fmt.Sprintf("https://example/%d", n), State: "open", Mergeable: true, HeadSHA: "sha-new"}
	return f.pr, nil
}
func (f *fakeProvider) GetPR(context.Context, int) (*provider.PR, error) {
	if f.pr == nil {
		return nil, errors.New("not found")
	}
	return f.pr, nil
}
func (f *fakeProvider) Checks(context.Context, *provider.PR) (provider.Checks, error) {
	return f.checks, nil
}

func newWorker(t *testing.T, f *fakeProvider) (*Worker, *queue.Queue) {
	t.Helper()
	cfg := &config.Config{
		Repo: "o/r", Provider: "github", Mirror: "/tmp/m.git",
		Queue: t.TempDir(), Base: "main", Merge: "when-green",
	}
	q := queue.New(cfg.Queue)
	if err := q.Ensure(); err != nil {
		t.Fatal(err)
	}
	w, err := New(cfg, f)
	if err != nil {
		t.Fatal(err)
	}
	return w, q
}

func TestOpenWaitsForChecksThenMerges(t *testing.T) {
	f := &fakeProvider{checks: provider.ChecksPending}
	w, q := newWorker(t, f)
	id, err := q.Enqueue(&queue.Request{Action: queue.ActionOpen, Head: "feature", Base: "main", Merge: "when-green"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := q.ReadResult(id)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pending || res.OK || f.mergeCalls != 0 || f.createCalls != 1 {
		t.Fatalf("first run: result=%+v create=%d merge=%d", res, f.createCalls, f.mergeCalls)
	}

	f.checks = provider.ChecksSuccess
	if _, err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err = q.ReadResult(id)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Pending || !res.Merged || f.mergeCalls != 1 {
		t.Fatalf("second run: result=%+v merge=%d", res, f.mergeCalls)
	}
	if pending, _ := q.Pending(); len(pending) != 0 {
		t.Fatalf("request not finalized: %+v", pending)
	}
}

func TestOpenDryRun(t *testing.T) {
	f := &fakeProvider{checks: provider.ChecksPending}
	w, q := newWorker(t, f)
	id, err := q.Enqueue(&queue.Request{Action: queue.ActionOpen, Head: "feature", Base: "main", Merge: "when-green", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := q.ReadResult(id)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || f.createCalls != 0 {
		t.Fatalf("result=%+v create=%d", res, f.createCalls)
	}
}

func TestMergeWhenGreenPending(t *testing.T) {
	f := &fakeProvider{pr: &provider.PR{Number: 2, State: "open", HeadSHA: "sha1"}, checks: provider.ChecksPending}
	w, q := newWorker(t, f)
	id, err := q.Enqueue(&queue.Request{Action: queue.ActionMerge, Number: 2, Merge: "when-green"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, _ := q.ReadResult(id)
	if !res.Pending || f.mergeCalls != 0 {
		t.Fatalf("result=%+v merge=%d", res, f.mergeCalls)
	}

	f.checks = provider.ChecksSuccess
	if _, err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, _ = q.ReadResult(id)
	if !res.OK || !res.Merged || f.mergeCalls != 1 {
		t.Fatalf("result=%+v merge=%d", res, f.mergeCalls)
	}
}

func TestOpenAlreadyMerged(t *testing.T) {
	f := &fakeProvider{
		pr:     &provider.PR{Number: 9, State: "closed", Merged: true, HeadSHA: "shaX"},
		checks: provider.ChecksSuccess,
	}
	w, q := newWorker(t, f)
	id, err := q.Enqueue(&queue.Request{Action: queue.ActionOpen, Head: "feature", Base: "main", HeadSHA: "shaX", Merge: "when-green"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := q.ReadResult(id)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || !res.Merged || f.createCalls != 0 || res.PR != 9 {
		t.Fatalf("result=%+v create=%d", res, f.createCalls)
	}
	if pending, _ := q.Pending(); len(pending) != 0 {
		t.Fatalf("request not finalized: %+v", pending)
	}
}

func TestOpenNewCommitAfterMerge(t *testing.T) {
	f := &fakeProvider{
		pr:     &provider.PR{Number: 10, State: "closed", Merged: true, HeadSHA: "old"},
		checks: provider.ChecksPending,
	}
	w, q := newWorker(t, f)
	id, err := q.Enqueue(&queue.Request{Action: queue.ActionOpen, Head: "feature", Base: "main", HeadSHA: "new", Merge: "when-green"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := q.ReadResult(id)
	if err != nil {
		t.Fatal(err)
	}
	if f.createCalls != 1 || f.pr == nil || f.pr.Number != 11 || res.PR != 11 {
		t.Fatalf("result=%+v create=%d pr=%+v", res, f.createCalls, f.pr)
	}
	if !res.Pending {
		t.Fatalf("expected pending checks, got %+v", res)
	}
}

func TestOpenNewCommitAfterClosed(t *testing.T) {
	f := &fakeProvider{
		pr:     &provider.PR{Number: 12, State: "closed", Merged: false, HeadSHA: "old"},
		checks: provider.ChecksPending,
	}
	w, q := newWorker(t, f)
	id, err := q.Enqueue(&queue.Request{Action: queue.ActionOpen, Head: "feature", Base: "main", HeadSHA: "new", Merge: "when-green"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := q.ReadResult(id)
	if err != nil {
		t.Fatal(err)
	}
	if f.createCalls != 1 || f.pr == nil || f.pr.Number != 13 || res.PR != 13 {
		t.Fatalf("result=%+v create=%d pr=%+v", res, f.createCalls, f.pr)
	}
}

func TestGitQueueWorker(t *testing.T) {
	mirror := filepath.Join(t.TempDir(), "mirror.git")
	if out, err := exec.Command("git", "init", "--bare", mirror).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	cfg := &config.Config{
		Repo: "o/r", Provider: "github", Mirror: mirror,
		Queue: "git", Base: "main", Merge: "when-green",
	}
	f := &fakeProvider{checks: provider.ChecksPending}
	w, err := New(cfg, f)
	if err != nil {
		t.Fatal(err)
	}
	q := queue.NewGit(mirror)
	id, err := q.Enqueue(&queue.Request{Action: queue.ActionOpen, Head: "feature", Base: "main", Merge: "when-green"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := q.ReadResult(id)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pending || f.mergeCalls != 0 {
		t.Fatalf("first run: %+v merge=%d", res, f.mergeCalls)
	}
	f.checks = provider.ChecksSuccess
	if _, err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err = q.ReadResult(id)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || !res.Merged || f.mergeCalls != 1 {
		t.Fatalf("second run: %+v merge=%d", res, f.mergeCalls)
	}
	if pending, _ := q.Pending(); len(pending) != 0 {
		t.Fatalf("request not deleted: %+v", pending)
	}
}

func TestStatusClosed(t *testing.T) {
	f := &fakeProvider{pr: &provider.PR{Number: 3, State: "closed", Merged: true}, checks: provider.ChecksPending}
	w, q := newWorker(t, f)
	id, err := q.Enqueue(&queue.Request{Action: queue.ActionStatus, Number: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, _ := q.ReadResult(id)
	if !res.OK || !res.Merged {
		t.Fatalf("result=%+v", res)
	}
}
