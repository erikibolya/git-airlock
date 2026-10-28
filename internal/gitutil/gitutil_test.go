package gitutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
