package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erikibolya/git-airlock/internal/config"
	"github.com/erikibolya/git-airlock/internal/provider"
	"github.com/erikibolya/git-airlock/internal/queue"
)

type fakeProvider struct {
	pr            *provider.PR
	checks        provider.Checks
	createCalls   int
	mergeCalls    int
	mergeStrategy string
}

func (f *fakeProvider) Name() string               { return "fake" }
func (f *fakeProvider) Ping(context.Context) error { return nil }
func (f *fakeProvider) Merge(_ context.Context, _ int, strategy string) error {
	f.mergeCalls++
	f.mergeStrategy = strategy
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

func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitTestFile(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, dir, "add", name)
	testGit(t, dir, "commit", "-m", msg)
}

// autoPRMirror builds a bare mirror with main, feature (ahead) and wip/x (ahead).
func autoPRMirror(t *testing.T) string {
	t.Helper()
	mirror := filepath.Join(t.TempDir(), "mirror.git")
	testGit(t, "", "init", "--bare", mirror)
	testGit(t, mirror, "symbolic-ref", "HEAD", "refs/heads/main")

	work := t.TempDir()
	testGit(t, work, "init")
	testGit(t, work, "checkout", "-b", "main")
	commitTestFile(t, work, "a.txt", "a", "base")
	testGit(t, work, "remote", "add", "origin", mirror)
	testGit(t, work, "push", "origin", "main")

	testGit(t, work, "checkout", "-b", "feature")
	commitTestFile(t, work, "b.txt", "b", "feature work")
	testGit(t, work, "push", "origin", "feature")

	testGit(t, work, "checkout", "main")
	testGit(t, work, "checkout", "-b", "wip/x")
	commitTestFile(t, work, "c.txt", "c", "scratch")
	testGit(t, work, "push", "origin", "wip/x")
	return mirror
}

func TestAutoOpenQueuesAheadBranches(t *testing.T) {
	mirror := autoPRMirror(t)
	cfg := &config.Config{
		Repo: "o/r", Provider: "github", Mirror: mirror, Queue: t.TempDir(),
		Base: "main", Merge: "when-green", MergeStrategy: "squash", AutoPR: true, AutoPRIgnore: []string{"wip/*"},
	}
	q := queue.New(cfg.Queue)
	if err := q.Ensure(); err != nil {
		t.Fatal(err)
	}
	f := &fakeProvider{checks: provider.ChecksSuccess}
	w, err := New(cfg, f)
	if err != nil {
		t.Fatal(err)
	}
	n, err := w.AutoOpen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("queued %d, want 1 (wip/x ignored, main is base)", n)
	}
	pending, err := q.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	req := pending[0]
	if req.Head != "feature" || req.Merge != "when-green" || req.Strategy != "squash" || req.HeadSHA == "" || req.Title != "feature work" {
		t.Fatalf("unexpected request: %+v", req)
	}

	// A second scan must not duplicate the queued request.
	if n, err := w.AutoOpen(context.Background()); err != nil || n != 0 {
		t.Fatalf("second AutoOpen = %d, %v; want 0", n, err)
	}
}

func TestAutoOpenSkipsOpenPR(t *testing.T) {
	mirror := autoPRMirror(t)
	cfg := &config.Config{
		Repo: "o/r", Provider: "github", Mirror: mirror, Queue: t.TempDir(),
		Base: "main", Merge: "when-green", AutoPR: true,
	}
	q := queue.New(cfg.Queue)
	if err := q.Ensure(); err != nil {
		t.Fatal(err)
	}
	// main is skipped as the base; feature and wip/x both see the same open PR.
	f := &fakeProvider{pr: &provider.PR{Number: 7, State: "open", HeadSHA: "abc"}, checks: provider.ChecksPending}
	w, err := New(cfg, f)
	if err != nil {
		t.Fatal(err)
	}
	n, err := w.AutoOpen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("queued %d, want 0 when an open PR exists", n)
	}
}

func TestMergeStrategy(t *testing.T) {
	ctx := context.Background()
	f := &fakeProvider{checks: provider.ChecksSuccess}
	w, q := newWorker(t, f)
	w.Cfg.MergeStrategy = "squash"
	if _, err := q.Enqueue(&queue.Request{Action: queue.ActionOpen, Head: "feature", Base: "main", Merge: "when-green"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if f.mergeStrategy != "squash" {
		t.Fatalf("strategy = %q, want squash", f.mergeStrategy)
	}

	f = &fakeProvider{checks: provider.ChecksSuccess}
	w, q = newWorker(t, f)
	w.Cfg.MergeStrategy = "squash"
	if _, err := q.Enqueue(&queue.Request{Action: queue.ActionOpen, Head: "feature", Base: "main", Merge: "when-green", Strategy: "rebase"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if f.mergeStrategy != "rebase" {
		t.Fatalf("request strategy = %q, want rebase", f.mergeStrategy)
	}
}

func TestUnknownMergeStrategy(t *testing.T) {
	f := &fakeProvider{checks: provider.ChecksSuccess}
	w, q := newWorker(t, f)
	id, err := q.Enqueue(&queue.Request{Action: queue.ActionOpen, Head: "feature", Base: "main", Merge: "when-green", Strategy: "fast-forward"})
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
	if res.OK || !strings.Contains(res.Message, "unknown merge strategy") {
		t.Fatalf("unexpected result: %+v", res)
	}
	if f.mergeCalls != 0 {
		t.Fatal("merged with an invalid strategy")
	}
}
