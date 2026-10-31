package gitutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func gitCmd(t *testing.T, dir string, args ...string) string {
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

func commitFile(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", name)
	gitCmd(t, dir, "commit", "-m", msg)
}

func TestParseRepo(t *testing.T) {
	cases := map[string]string{
		"https://github.com/owner/name.git":         "owner/name",
		"https://user:tok@example.com/o/n":          "o/n",
		"git@github.com:owner/name.git":             "owner/name",
		"ssh://git@example.com:2222/owner/name.git": "owner/name",
	}
	for in, want := range cases {
		got, err := ParseRepo(in)
		if err != nil || got != want {
			t.Fatalf("%s: got %q %v, want %q", in, got, err, want)
		}
	}
}

func TestSyncFastForwardAndDiverged(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	gitCmd(t, "", "init", "--bare", remote)

	work := filepath.Join(base, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "init")
	gitCmd(t, work, "config", "user.email", "test@example.com")
	gitCmd(t, work, "config", "user.name", "Test")
	gitCmd(t, work, "checkout", "-b", "main")
	commitFile(t, work, "a.txt", "a", "a")
	gitCmd(t, work, "remote", "add", "origin", remote)
	gitCmd(t, work, "push", "-u", "origin", "main")
	gitCmd(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")

	mirror := filepath.Join(base, "mirror.git")
	gitCmd(t, "", "clone", "--bare", work, mirror)
	gitCmd(t, mirror, "remote", "set-url", "origin", remote)

	// The agent pushes to the mirror.
	agent := filepath.Join(base, "agent")
	gitCmd(t, "", "clone", mirror, agent)
	gitCmd(t, agent, "config", "user.email", "test@example.com")
	gitCmd(t, agent, "config", "user.name", "Test")
	commitFile(t, agent, "b.txt", "b", "b")
	gitCmd(t, agent, "push", "origin", "main")

	report, err := Sync(ctx, mirror, "origin")
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if len(report.Pushed) != 1 || report.Pushed[0] != "main" {
		t.Fatalf("expected main pushed, got %+v", report)
	}
	want := gitCmd(t, agent, "rev-parse", "HEAD")
	if got := gitCmd(t, remote, "rev-parse", "main"); got != want {
		t.Fatalf("remote not updated: %s != %s", got, want)
	}

	// Someone else pushes to the private remote; the mirror must fast-forward.
	other := filepath.Join(base, "other")
	gitCmd(t, "", "clone", remote, other)
	gitCmd(t, other, "config", "user.email", "test@example.com")
	gitCmd(t, other, "config", "user.name", "Test")
	commitFile(t, other, "c.txt", "c", "c")
	gitCmd(t, other, "push", "origin", "main")

	report, err = Sync(ctx, mirror, "origin")
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if len(report.FastForwarded) != 1 || report.FastForwarded[0] != "main" {
		t.Fatalf("expected main fast-forwarded, got %+v", report)
	}
	if got, want := gitCmd(t, mirror, "rev-parse", "main"), gitCmd(t, remote, "rev-parse", "main"); got != want {
		t.Fatalf("mirror not fast-forwarded: %s != %s", got, want)
	}

	// Diverge: agent and remote both commit on top of different tips.
	gitCmd(t, agent, "fetch", "origin")
	gitCmd(t, agent, "reset", "--hard", "origin/main")
	commitFile(t, agent, "d.txt", "d", "d")
	gitCmd(t, agent, "push", "origin", "main")

	commitFile(t, other, "e.txt", "e", "e")
	gitCmd(t, other, "push", "origin", "main")

	report, err = Sync(ctx, mirror, "origin")
	if !errors.Is(err, ErrDiverged) {
		t.Fatalf("expected ErrDiverged, got %v (%+v)", err, report)
	}
	if len(report.Diverged) != 1 || report.Diverged[0] != "main" {
		t.Fatalf("expected diverged main, got %+v", report)
	}
}

func TestBranchesAhead(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	gitCmd(t, dir, "init", "--bare")
	gitCmd(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")

	work := t.TempDir()
	gitCmd(t, work, "init")
	gitCmd(t, work, "checkout", "-b", "main")
	commitFile(t, work, "a.txt", "a", "base")
	gitCmd(t, work, "remote", "add", "origin", dir)
	gitCmd(t, work, "push", "origin", "main")

	gitCmd(t, work, "checkout", "-b", "feature")
	commitFile(t, work, "b.txt", "b", "feature work")
	gitCmd(t, work, "push", "origin", "feature")

	gitCmd(t, work, "checkout", "main")
	gitCmd(t, work, "checkout", "-b", "merged")
	commitFile(t, work, "c.txt", "c", "already merged")
	gitCmd(t, work, "push", "origin", "merged")
	gitCmd(t, work, "checkout", "main")
	gitCmd(t, work, "merge", "--no-ff", "merged", "-m", "merge merged")
	gitCmd(t, work, "push", "origin", "main")
	gitCmd(t, work, "push", "origin", "merged")

	got, err := BranchesAhead(ctx, dir, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "feature" {
		t.Fatalf("got %+v, want only feature", got)
	}
	if got[0].Subject != "feature work" {
		t.Fatalf("subject = %q", got[0].Subject)
	}
	if len(got[0].SHA) < 7 {
		t.Fatalf("sha = %q", got[0].SHA)
	}
}

func TestBranchesAheadSkipsRemoteBase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	gitCmd(t, dir, "init", "--bare")
	gitCmd(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")

	work := t.TempDir()
	gitCmd(t, work, "init")
	gitCmd(t, work, "checkout", "-b", "main")
	commitFile(t, work, "a.txt", "a", "base")
	gitCmd(t, work, "remote", "add", "origin", dir)
	gitCmd(t, work, "push", "origin", "main")
	baseSHA := gitCmd(t, work, "rev-parse", "HEAD")

	// The remote-tracking base lags behind: local main has a commit the sync has not
	// pushed yet. main is still the base and must not be reported as ahead of itself.
	gitCmd(t, dir, "update-ref", "refs/remotes/origin/main", baseSHA)
	commitFile(t, work, "local.txt", "local", "local only")
	gitCmd(t, work, "push", "origin", "main")

	got, err := BranchesAhead(ctx, dir, "refs/remotes/origin/main")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want no branch (main is the base)", got)
	}
}

func TestSyncOverSSH(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	shared := filepath.Join(base, "shared.git")
	company := filepath.Join(base, "company.git")
	cache := filepath.Join(base, "cache.git")
	gitCmd(t, "", "init", "--bare", shared)
	gitCmd(t, shared, "symbolic-ref", "HEAD", "refs/heads/main")
	gitCmd(t, "", "init", "--bare", company)
	gitCmd(t, company, "symbolic-ref", "HEAD", "refs/heads/main")

	work := filepath.Join(base, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "init")
	gitCmd(t, work, "checkout", "-b", "main")
	commitFile(t, work, "a.txt", "a", "initial")
	gitCmd(t, work, "remote", "add", "shared", shared)
	gitCmd(t, work, "push", "shared", "main")
	initial := gitCmd(t, work, "rev-parse", "HEAD")

	report, err := SyncOverSSH(ctx, cache, shared, company, "company")
	if err != nil {
		t.Fatal(err)
	}
	if got := gitCmd(t, company, "rev-parse", "refs/heads/main"); got != initial {
		t.Fatalf("company main = %s, want %s (report %+v)", got, initial, report)
	}
	if got := gitCmd(t, cache, "rev-parse", "refs/heads/main"); got != initial {
		t.Fatalf("cache main = %s", got)
	}

	// A branch created only on the private remote is bridged back to the mirror.
	other := filepath.Join(base, "other")
	gitCmd(t, "", "clone", company, other)
	gitCmd(t, other, "checkout", "-b", "extra")
	commitFile(t, other, "b.txt", "b", "extra work")
	gitCmd(t, other, "push", "origin", "extra")
	if _, err := SyncOverSSH(ctx, cache, shared, company, "company"); err != nil {
		t.Fatal(err)
	}
	if got := gitCmd(t, shared, "rev-parse", "refs/heads/extra"); got == "" {
		t.Fatal("extra was not bridged to shared")
	}

	// A commit pushed to company fast-forwards the mirror.
	gitCmd(t, other, "checkout", "main")
	commitFile(t, other, "c.txt", "c", "company ahead")
	gitCmd(t, other, "push", "origin", "main")
	companyTip := gitCmd(t, other, "rev-parse", "HEAD")
	if _, err := SyncOverSSH(ctx, cache, shared, company, "company"); err != nil {
		t.Fatal(err)
	}
	if got := gitCmd(t, shared, "rev-parse", "refs/heads/main"); got != companyTip {
		t.Fatalf("shared main = %s, want %s", got, companyTip)
	}

	// Divergence is reported, not force-pushed.
	sharedWork := filepath.Join(base, "sharedwork")
	gitCmd(t, "", "clone", shared, sharedWork)
	gitCmd(t, sharedWork, "checkout", "main")
	commitFile(t, sharedWork, "d.txt", "d", "shared ahead")
	gitCmd(t, sharedWork, "push", "origin", "main")
	gitCmd(t, other, "checkout", "main")
	commitFile(t, other, "e.txt", "e", "company ahead again")
	gitCmd(t, other, "push", "origin", "main")

	report, err = SyncOverSSH(ctx, cache, shared, company, "company")
	if !errors.Is(err, ErrDiverged) || len(report.Diverged) == 0 {
		t.Fatalf("want divergence, got %v %+v", err, report)
	}
}

// A push whose response stalls after Gitea accepted the ref must not be
// reported as failed: the branch is already on the remote and the worker would
// otherwise skip auto-PR and retry a push that already landed.
func TestPushVerifiesRemoteAfterStalledResponse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell shim")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	work := filepath.Join(base, "work")
	gitCmd(t, "", "init", "--bare", remote)
	gitCmd(t, "", "init", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, work, "add", "a.txt")
	gitCmd(t, work, "commit", "-m", "initial")
	gitCmd(t, work, "remote", "add", "company", remote)

	shimDir := filepath.Join(base, "shim")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	quoted := strings.ReplaceAll(realGit, "'", `'\''`)
	shim := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = push ]; then\n  '%s' \"$@\" || exit $?\n  exit 1\nfi\nexec '%s' \"$@\"\n", quoted, quoted)
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := push(context.Background(), work, "company", "refs/heads/main", "main"); err != nil {
		t.Fatalf("push returned error even though the remote ref matches: %v", err)
	}
	local := strings.TrimSpace(gitCmd(t, work, "rev-parse", "refs/heads/main"))
	got := strings.TrimSpace(gitCmd(t, "", "--git-dir", remote, "rev-parse", "refs/heads/main"))
	if got != local {
		t.Fatalf("remote main = %s, want %s", got, local)
	}
}
