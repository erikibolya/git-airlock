package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erikibolya/git-airlock/internal/config"
	"github.com/erikibolya/git-airlock/internal/provider"
	"github.com/erikibolya/git-airlock/internal/queue"
	"github.com/erikibolya/git-airlock/internal/worker"
)

// stubProvider reports "no pull request" for every branch and accepts creations.
type stubProvider struct {
	pr *provider.PR
}

func (s *stubProvider) Name() string               { return "stub" }
func (s *stubProvider) Ping(context.Context) error { return nil }
func (s *stubProvider) FindOpenPR(context.Context, string, string) (*provider.PR, error) {
	return nil, nil
}
func (s *stubProvider) FindPR(context.Context, string, string) (*provider.PR, error) {
	return nil, nil
}
func (s *stubProvider) ListPRs(context.Context, string, string) ([]*provider.PR, error) {
	return nil, nil
}
func (s *stubProvider) CreatePR(_ context.Context, req provider.CreateRequest) (*provider.PR, error) {
	if s.pr == nil {
		s.pr = &provider.PR{Number: 1, URL: "https://example/1", State: "open", Mergeable: true, Head: req.Head, HeadSHA: "sha"}
	}
	return s.pr, nil
}
func (s *stubProvider) GetPR(context.Context, int) (*provider.PR, error) {
	if s.pr == nil {
		return nil, errors.New("not found")
	}
	return s.pr, nil
}
func (s *stubProvider) Checks(context.Context, *provider.PR) (provider.Checks, error) {
	return provider.ChecksNone, nil
}
func (s *stubProvider) Merge(context.Context, int, string) error   { return nil }
func (s *stubProvider) DeleteBranch(context.Context, string) error { return nil }

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

// airlockMirror builds a bare mirror with main and feature, feature being ahead of main.
func airlockMirror(t *testing.T) string {
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
	return mirror
}

func newCycleWorker(t *testing.T, mirror string) (*repoWorker, *queue.Queue) {
	t.Helper()
	cfg := &config.Config{
		Repo: "o/r", Provider: "github", Mirror: mirror, Queue: t.TempDir(),
		Remote: "company", Base: "main", Merge: "never", AutoPR: true,
	}
	q := queue.New(cfg.Queue)
	if err := q.Ensure(); err != nil {
		t.Fatal(err)
	}
	w, err := worker.New(cfg, &stubProvider{})
	if err != nil {
		t.Fatal(err)
	}
	return &repoWorker{cfg: cfg, path: "test", w: w}, q
}

func pendingHeads(t *testing.T, q *queue.Queue) []string {
	t.Helper()
	pending, err := q.Pending()
	if err != nil {
		t.Fatal(err)
	}
	heads := make([]string, 0, len(pending))
	for _, req := range pending {
		heads = append(heads, req.Head)
	}
	return heads
}

// A failed mirror sync leaves every new branch missing on the remote. Proposing pull
// requests for them then fails with Gitea's generic 404 ("head ref not found") once per
// branch per run, forever, so no proposal may be queued until a sync succeeds.
func TestRunRepoCycleSkipsAutoOpenAfterFailedSync(t *testing.T) {
	// No "company" remote: reposync.Sync fails.
	mirror := airlockMirror(t)
	r, q := newCycleWorker(t, mirror)

	if n := runRepoCycle(context.Background(), r, false); n != 0 {
		t.Fatalf("queued %d pull request(s) after a failed sync, want none", n)
	}
	if heads := pendingHeads(t, q); len(heads) != 0 {
		t.Fatalf("pending %v after a failed sync, want nothing", heads)
	}
}

// A successful sync must still propose new branches: the guard above must not disable
// auto PR altogether.
func TestRunRepoCycleAutoOpensAfterSuccessfulSync(t *testing.T) {
	remote := filepath.Join(t.TempDir(), "remote.git")
	testGit(t, "", "init", "--bare", remote)
	mirror := airlockMirror(t)
	testGit(t, mirror, "remote", "add", "company", remote)

	r, _ := newCycleWorker(t, mirror)
	if n := runRepoCycle(context.Background(), r, false); n != 1 {
		t.Fatalf("queued %d pull request(s) after a successful sync, want 1 (feature)", n)
	}
}

// --no-sync means the caller already synced the mirror; auto PR keeps working.
func TestRunRepoCycleNoSyncStillAutoOpens(t *testing.T) {
	mirror := airlockMirror(t)
	r, _ := newCycleWorker(t, mirror)

	if n := runRepoCycle(context.Background(), r, true); n != 1 {
		t.Fatalf("queued %d pull request(s) with --no-sync, want 1 (feature)", n)
	}
}
