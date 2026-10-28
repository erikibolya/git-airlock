package queue

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/erikibolya/git-airlock/internal/proc"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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
	relaxRef(filepath.Join(g.repo, filepath.FromSlash(requestsRef+req.ID)))
	return req.ID, nil
}

func (g *GitStore) Pending() ([]Request, error) {
	out, err := g.git(context.Background(), "", "for-each-ref", "--format=%(refname) %(objectname)", strings.TrimSuffix(requestsRef, "/"))
	if err != nil {
		return nil, err
	}
	type pair struct {
		id  string
		sha string
	}
	var refs []pair
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[0], requestsRef) {
			continue
		}
		refs = append(refs, pair{id: strings.TrimPrefix(fields[0], requestsRef), sha: fields[1]})
	}
	shas := make([]string, len(refs))
	for i, r := range refs {
		shas[i] = r.sha
	}
	objects, err := g.readObjects(shas)
	if err != nil {
		return nil, err
	}
	outReqs := make([]Request, 0, len(refs))
	for _, ref := range refs {
		data, ok := objects[ref.sha]
		if !ok {
			outReqs = append(outReqs, Request{ID: ref.id, Action: ActionBroken})
			continue
		}
		var req Request
		if err := json.Unmarshal(data, &req); err != nil {
			outReqs = append(outReqs, Request{ID: ref.id, Action: ActionBroken})
			continue
		}
		if req.ID == "" {
			req.ID = ref.id
		}
		outReqs = append(outReqs, req)
	}
	return outReqs, nil
}

// readObjects reads many blobs with a single cat-file --batch process. This
// matters on SMB/NFS mirrors where every git invocation is expensive.
func (g *GitStore) readObjects(shas []string) (map[string][]byte, error) {
	if len(shas) == 0 {
		return map[string][]byte{}, nil
	}
	var in strings.Builder
	for _, sha := range shas {
		in.WriteString(sha)
		in.WriteByte('\n')
	}
	out, err := g.gitOutput(context.Background(), in.String(), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	objects := make(map[string][]byte, len(shas))
	reader := bufio.NewReader(strings.NewReader(string(out)))
	for {
		header, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		fields := strings.Fields(header)
		if len(fields) < 3 {
			continue
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil {
			break
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(reader, data); err != nil {
			break
		}
		objects[fields[0]] = data
		_, _ = reader.ReadByte()
	}
	return objects, nil
}

func (g *GitStore) gitOutput(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := proc.CommandContext(runCtx, "git", append([]string{"-C", g.repo}, args...)...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return out, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
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
	if _, err = g.git(context.Background(), "", "update-ref", resultsRef+res.ID, sha); err != nil {
		return err
	}
	relaxRef(filepath.Join(g.repo, filepath.FromSlash(resultsRef+res.ID)))
	return nil
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

// ListResults returns the newest results first.
func (g *GitStore) ListResults(limit int) ([]Result, error) {
	out, err := g.git(context.Background(), "", "for-each-ref", "--format=%(refname) %(objectname)", strings.TrimSuffix(resultsRef, "/"))
	if err != nil {
		return nil, err
	}
	type pair struct {
		ref string
		sha string
	}
	var pairs []pair
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[0], resultsRef) {
			continue
		}
		pairs = append(pairs, pair{ref: fields[0], sha: fields[1]})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].ref > pairs[j].ref })
	if limit <= 0 {
		limit = 20
	}
	shas := make([]string, 0, limit)
	for _, p := range pairs {
		if len(shas) >= limit {
			break
		}
		shas = append(shas, p.sha)
	}
	objects, err := g.readObjects(shas)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(shas))
	for _, p := range pairs {
		if len(results) >= limit {
			break
		}
		data, ok := objects[p.sha]
		if !ok {
			continue
		}
		var res Result
		if json.Unmarshal(data, &res) != nil {
			continue
		}
		results = append(results, res)
	}
	return results, nil
}

// LogPath is the local worker log file.
func (g *GitStore) LogPath() string {
	return g.logPath()
}

// TailLog returns the last lines of the local worker log.
func (g *GitStore) TailLog(lines int) (string, error) {
	path := g.logPath()
	if path == "" {
		return "", nil
	}
	return tailFile(path, lines)
}

func (g *GitStore) logPath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	name := strings.TrimSuffix(filepath.Base(filepath.Clean(g.repo)), ".git")
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, name)
	if name == "" || name == "." {
		name = "repo"
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(g.repo))
	return filepath.Join(dir, "git-airlock", fmt.Sprintf("worker-%s-%08x.log", name, h.Sum32()))
}

func (g *GitStore) Log(format string, args ...any) {
	path := g.logPath()
	if path == "" {
		return
	}
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
	cmd := proc.CommandContext(runCtx, "git", append([]string{"-C", g.repo}, args...)...)
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
