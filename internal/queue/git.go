package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	requestsRef = "refs/airlock/requests/"
	resultsRef  = "refs/airlock/results/"
)

// GitStore keeps requests and results as blobs referenced by refs in the bare
// mirror. It needs no separate shared directory: the mirror is already the
// transport both sides can reach.
type GitStore struct {
	repo string
}

// NewGit returns a git-ref store rooted at a bare mirror.
func NewGit(repo string) *GitStore {
	return &GitStore{repo: repo}
}

func (g *GitStore) Ensure() error {
	out, err := g.git(context.Background(), "", "rev-parse", "--is-bare-repository")
	if err != nil {
		return err
	}
	if out != "true" {
		return fmt.Errorf("%s is not a bare repository", g.repo)
	}
	return nil
}

func (g *GitStore) Location(id string) string {
	return fmt.Sprintf("%s (%s%s)", g.repo, resultsRef, id)
}

func (g *GitStore) Enqueue(req *Request) (string, error) {
	if err := g.Ensure(); err != nil {
		return "", err
	}
	if req.ID == "" {
		req.ID = NewID()
	}
	data, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	sha, err := g.git(context.Background(), string(data), "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	if _, err := g.git(context.Background(), "", "update-ref", requestsRef+req.ID, sha); err != nil {
		return "", err
	}
	return req.ID, nil
}

func (g *GitStore) Pending() ([]Request, error) {
	out, err := g.git(context.Background(), "", "for-each-ref", "--format=%(refname) %(objectname)", strings.TrimSuffix(requestsRef, "/"))
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[0], requestsRef) {
			continue
		}
		refs = append(refs, line)
	}
	outReqs := make([]Request, 0, len(refs))
	for _, line := range refs {
		fields := strings.Fields(line)
		id := strings.TrimPrefix(fields[0], requestsRef)
		data, err := g.git(context.Background(), "", "cat-file", "-p", fields[1])
		if err != nil {
			outReqs = append(outReqs, Request{ID: id, Action: ActionBroken})
			continue
		}
		var req Request
		if err := json.Unmarshal([]byte(data), &req); err != nil {
			outReqs = append(outReqs, Request{ID: id, Action: ActionBroken})
			continue
		}
		if req.ID == "" {
			req.ID = id
		}
		outReqs = append(outReqs, req)
	}
	return outReqs, nil
}

func (g *GitStore) WriteResult(res *Result) error {
	if res.ID == "" {
		return errors.New("result has no id")
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	sha, err := g.git(context.Background(), string(data), "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	_, err = g.git(context.Background(), "", "update-ref", resultsRef+res.ID, sha)
	return err
}

func (g *GitStore) ReadResult(id string) (*Result, error) {
	ref := resultsRef + id
	if _, err := g.git(context.Background(), "", "rev-parse", "--verify", "--quiet", ref); err != nil {
		return nil, os.ErrNotExist
	}
	data, err := g.git(context.Background(), "", "cat-file", "-p", ref)
	if err != nil {
		return nil, err
	}
	var res Result
	if err := json.Unmarshal([]byte(data), &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (g *GitStore) MarkProcessed(id string) error {
	_, err := g.git(context.Background(), "", "update-ref", "-d", requestsRef+id)
	return err
}

func (g *GitStore) Wait(ctx context.Context, id string, interval time.Duration, onChange func(*Result)) (*Result, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var last string
	for {
		res, err := g.ReadResult(id)
		if err == nil {
			encoded, _ := json.Marshal(res)
			if string(encoded) != last {
				last = string(encoded)
				if onChange != nil {
					onChange(res)
				}
			}
			if res.OK || !res.Pending {
				return res, nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (g *GitStore) Log(format string, args ...any) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return
	}
	path := filepath.Join(dir, "git-airlock", "worker.log")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if st, err := os.Stat(path); err == nil && st.Size() > 1<<20 {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

func (g *GitStore) git(ctx context.Context, stdin string, args ...string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "git", append([]string{"-C", g.repo}, args...)...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
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
