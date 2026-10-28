// Package gitutil wraps the git commands Git Airlock needs.
package gitutil

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/erikibolya/git-airlock/internal/proc"
)

const (
	networkTimeout = 90 * time.Second
	localTimeout   = 60 * time.Second
)

// SyncReport describes what a mirror sync changed.
type SyncReport struct {
	Pushed        []string
	FastForwarded []string
	Diverged      []string
	Warnings      []string
}

// ErrDiverged means at least one branch needs a manual merge/rebase.
var ErrDiverged = errors.New("one or more branches diverged")

// Root returns the working tree root of the current directory.
func Root() (string, error) {
	out, err := run(context.Background(), "", localTimeout, false, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return out, nil
}

// CurrentBranch returns the checked-out branch name.
func CurrentBranch(dir string) (string, error) {
	return run(context.Background(), dir, localTimeout, false, "symbolic-ref", "--quiet", "--short", "HEAD")
}

// HeadSubject returns the subject of the current HEAD commit.
func HeadSubject(dir string) (string, error) {
	return run(context.Background(), dir, localTimeout, false, "log", "-1", "--pretty=%s")
}

// RemoteURL returns the URL of a configured remote.
func RemoteURL(dir, name string) (string, error) {
	return run(context.Background(), dir, localTimeout, false, "remote", "get-url", name)
}

// RemoteExists reports whether a remote is configured.
func RemoteExists(dir, name string) bool {
	_, err := RemoteURL(dir, name)
	return err == nil
}

// ParseRepo extracts owner/name from common git remote URLs.
func ParseRepo(raw string) (string, error) {
	s := strings.TrimSpace(strings.TrimSuffix(raw, ".git"))
	if s == "" {
		return "", errors.New("empty remote URL")
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "ssh://") || strings.HasPrefix(s, "git://") {
		u, err := url.Parse(s)
		if err != nil {
			return "", err
		}
		parts := splitPath(u.Path)
		if len(parts) < 2 {
			return "", fmt.Errorf("cannot find owner/name in %q", raw)
		}
		return parts[len(parts)-2] + "/" + parts[len(parts)-1], nil
	}
	// scp-like: git@host:owner/name
	if i := strings.LastIndex(s, ":"); i > 0 && !strings.Contains(s[:i], "/") {
		parts := splitPath(s[i+1:])
		if len(parts) >= 2 {
			return parts[len(parts)-2] + "/" + parts[len(parts)-1], nil
		}
	}
	return "", fmt.Errorf("cannot find owner/name in %q", raw)
}

func splitPath(p string) []string {
	var out []string
	for _, s := range strings.Split(strings.Trim(p, "/"), "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// DefaultBranch guesses the default branch from refs/remotes/origin/HEAD.
func DefaultBranch(dir string) string {
	if out, err := run(context.Background(), dir, localTimeout, false, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		out = strings.TrimPrefix(out, "origin/")
		if out != "" {
			return out
		}
	}
	for _, candidate := range []string{"main", "master"} {
		if _, err := run(context.Background(), dir, localTimeout, false, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+candidate); err == nil {
			return candidate
		}
	}
	return "main"
}

// CloneBare creates a bare mirror from a local working copy.
func CloneBare(ctx context.Context, src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	_, err := run(ctx, "", 5*time.Minute, false, "clone", "--bare", "--quiet", src, dst)
	return err
}

// ConfigureRemotes points the working copy at the mirror and keeps the
// original URL as the private remote. It returns the original URL.
func ConfigureRemotes(ctx context.Context, root, mirror, remoteName string) (string, error) {
	mirrorAbs, err := filepath.Abs(mirror)
	if err != nil {
		return "", err
	}
	originURL, err := RemoteURL(root, "origin")
	if err != nil {
		return "", fmt.Errorf("no origin remote: %w", err)
	}
	sameAsMirror := filepath.Clean(originURL) == filepath.Clean(mirrorAbs)

	if sameAsMirror {
		// Re-run: origin is already the mirror, recover the private URL from
		// the remote name if the user kept it.
		privateURL, err := RemoteURL(root, remoteName)
		if err != nil {
			return "", fmt.Errorf("origin already points to the mirror; add the private remote %q manually", remoteName)
		}
		originURL = privateURL
	} else {
		if remoteName == "" || remoteName == "origin" {
			return "", errors.New("remote name must not be origin")
		}
		if RemoteExists(root, remoteName) {
			return "", fmt.Errorf("remote %q already exists", remoteName)
		}
		if _, err := run(ctx, root, localTimeout, false, "remote", "rename", "origin", remoteName); err != nil {
			return "", err
		}
		originURL, err = RemoteURL(root, remoteName)
		if err != nil {
			return "", err
		}
		if _, err := run(ctx, root, localTimeout, false, "remote", "add", "origin", mirrorAbs); err != nil {
			return "", err
		}
	}

	if !RemoteExists(mirrorAbs, remoteName) {
		if _, err := run(ctx, mirrorAbs, localTimeout, false, "remote", "add", remoteName, originURL); err != nil {
			return "", err
		}
	}
	return originURL, nil
}

// Sync performs a fast-forward-only two-way sync between the mirror and the
// private remote. It never force-pushes or rewinds a branch.
func Sync(ctx context.Context, mirror, remote string) (SyncReport, error) {
	var report SyncReport

	bare, err := run(ctx, mirror, localTimeout, false, "rev-parse", "--is-bare-repository")
	if err != nil {
		return report, fmt.Errorf("mirror %s: %w", mirror, err)
	}
	if strings.TrimSpace(bare) != "true" {
		return report, fmt.Errorf("mirror %s is not a bare repository", mirror)
	}

	fetchCtx, cancel := context.WithTimeout(ctx, networkTimeout)
	defer cancel()
	if _, err := run(fetchCtx, mirror, networkTimeout, true, "fetch", "--prune", remote, "+refs/heads/*:refs/remotes/"+remote+"/*"); err != nil {
		return report, fmt.Errorf("fetch: %w", err)
	}

	refs, err := run(ctx, mirror, localTimeout, false, "for-each-ref", "--format=%(refname)", "refs/heads")
	if err != nil {
		return report, err
	}
	var branches []string
	for _, ref := range strings.Split(refs, "\n") {
		ref = strings.TrimSpace(ref)
		if ref != "" {
			branches = append(branches, ref)
		}
	}
	sort.Strings(branches)

	remoteHeads, err := run(ctx, mirror, localTimeout, false, "for-each-ref", "--format=%(refname)", "refs/remotes/"+remote)
	if err != nil {
		return report, err
	}
	for _, remoteRef := range strings.Split(remoteHeads, "\n") {
		remoteRef = strings.TrimSpace(remoteRef)
		if remoteRef == "" || strings.HasSuffix(remoteRef, "/HEAD") {
			continue
		}
		branch := strings.TrimPrefix(remoteRef, "refs/remotes/"+remote+"/")
		localRef := "refs/heads/" + branch
		localSHA, _ := revParse(ctx, mirror, localRef)
		remoteSHA, _ := revParse(ctx, mirror, remoteRef)
		if localSHA == "" || remoteSHA == "" || localSHA == remoteSHA {
			continue
		}
		ancestor, err := isAncestor(ctx, mirror, localSHA, remoteSHA)
		if err != nil {
			return report, err
		}
		if !ancestor {
			continue // handled by the push loop below or diverged
		}
		pushCtx, cancel := context.WithTimeout(ctx, networkTimeout)
		_, err = run(pushCtx, mirror, networkTimeout, true, "update-ref", localRef, remoteSHA, localSHA)
		cancel()
		if err != nil {
			return report, fmt.Errorf("fast-forward %s: %w", branch, err)
		}
		report.FastForwarded = append(report.FastForwarded, branch)
	}

	for _, ref := range branches {
		branch := strings.TrimPrefix(ref, "refs/heads/")
		remoteRef := "refs/remotes/" + remote + "/" + branch
		localSHA, err := revParse(ctx, mirror, ref)
		if err != nil || localSHA == "" {
			continue
		}
		remoteSHA, _ := revParse(ctx, mirror, remoteRef)

		if remoteSHA == "" {
			if err := push(ctx, mirror, remote, ref, branch); err != nil {
				return report, err
			}
			report.Pushed = append(report.Pushed, branch)
			continue
		}
		if localSHA == remoteSHA {
			continue
		}
		remoteBehind, err := isAncestor(ctx, mirror, remoteSHA, localSHA)
		if err != nil {
			return report, err
		}
		if remoteBehind {
			if err := push(ctx, mirror, remote, ref, branch); err != nil {
				return report, err
			}
			report.Pushed = append(report.Pushed, branch)
			continue
		}
		localBehind, err := isAncestor(ctx, mirror, localSHA, remoteSHA)
		if err != nil {
			return report, err
		}
		if localBehind {
			continue // already fast-forwarded above
		}
		report.Diverged = append(report.Diverged, branch)
	}

	if _, err := run(ctx, mirror, networkTimeout, true, "push", "--tags", remote); err != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("tags: %v", err))
	}

	if len(report.Diverged) > 0 {
		return report, ErrDiverged
	}
	return report, nil
}

func push(ctx context.Context, dir, remote, ref, branch string) error {
	pushCtx, cancel := context.WithTimeout(ctx, networkTimeout)
	defer cancel()
	_, err := run(pushCtx, dir, networkTimeout, true, "push", remote, ref+":refs/heads/"+branch)
	if err != nil {
		return fmt.Errorf("push %s: %w", branch, err)
	}
	return nil
}

func revParse(ctx context.Context, dir, ref string) (string, error) {
	out, err := run(ctx, dir, localTimeout, false, "rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func isAncestor(ctx context.Context, dir, a, b string) (bool, error) {
	cmd := proc.CommandContext(ctx, "git", "-C", dir, "merge-base", "--is-ancestor", a, b)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// Branch is a local branch with its tip and subject.
type Branch struct {
	Name    string
	SHA     string
	Subject string
}

// RefExists reports whether ref resolves in dir.
func RefExists(dir, ref string) bool {
	out, err := revParse(context.Background(), dir, ref)
	return err == nil && out != ""
}

// BranchesAhead lists local branches whose tip is not contained in baseRef.
// baseRef may be a local or remote-tracking ref; an unknown baseRef makes every
// branch a candidate.
func BranchesAhead(ctx context.Context, dir, baseRef string) ([]Branch, error) {
	out, err := run(ctx, dir, localTimeout, false, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, err
	}
	baseSHA := ""
	if baseRef != "" {
		baseSHA, _ = revParse(ctx, dir, baseRef)
	}
	baseName := strings.TrimPrefix(baseRef, "refs/heads/")
	var branches []Branch
	for _, name := range strings.Split(out, "\n") {
		name = strings.TrimSpace(name)
		if name == "" || name == baseName {
			continue
		}
		sha, err := revParse(ctx, dir, "refs/heads/"+name)
		if err != nil || sha == "" {
			continue
		}
		if baseSHA != "" {
			ancestor, err := isAncestor(ctx, dir, sha, baseSHA)
			if err != nil {
				return nil, err
			}
			if ancestor {
				continue
			}
		}
		subject, _ := run(ctx, dir, localTimeout, false, "log", "-1", "--pretty=%s", sha)
		branches = append(branches, Branch{Name: name, SHA: sha, Subject: subject})
	}
	return branches, nil
}

func run(ctx context.Context, dir string, timeout time.Duration, network bool, args ...string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := proc.CommandContext(runCtx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if network {
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	}
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return text, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, text)
	}
	return text, nil
}
