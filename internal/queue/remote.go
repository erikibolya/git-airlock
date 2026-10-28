package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RemoteStore keeps refs/airlock/* in a remote bare repository (file://, ssh://
// or https://) and mirrors them into a local cache. It removes the need for a
// shared filesystem between the isolated host and the worker.
type RemoteStore struct {
	remote string
	cache  string
}

// NewRemote returns a git-ref queue that talks to a remote repository.
func NewRemote(remote string) *RemoteStore {
	return newRemote(remote, defaultRemoteCache(remote))
}

func newRemote(remote, cache string) *RemoteStore {
	return &RemoteStore{remote: remote, cache: cache}
}

func defaultRemoteCache(remote string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(remote))
	return filepath.Join(dir, "git-airlock", fmt.Sprintf("queue-%08x.git", h.Sum32()))
}

func (r *RemoteStore) Ensure() error {
	if _, err := os.Stat(r.cache); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(r.cache), 0o755); err != nil {
			return err
		}
		if err := r.initCache(); err != nil {
			return err
		}
	}
	out, err := r.local("", "rev-parse", "--is-bare-repository")
	if err != nil {
		return err
	}
	if out != "true" {
		return fmt.Errorf("%s is not a bare repository", r.cache)
	}
	return nil
}

func (r *RemoteStore) Location(id string) string {
	return fmt.Sprintf("%s (%s%s)", r.remote, resultsRef, id)
}

func (r *RemoteStore) Enqueue(req *Request) (string, error) {
	if err := r.Ensure(); err != nil {
		return "", err
	}
	if req.ID == "" {
		req.ID = NewID()
	}
	sha, err := r.hash(req)
	if err != nil {
		return "", err
	}
	if _, err := r.net("push", r.remote, sha+":"+requestsRef+req.ID); err != nil {
		return "", err
	}
	return req.ID, nil
}

func (r *RemoteStore) Pending() ([]Request, error) {
	if err := r.Ensure(); err != nil {
		return nil, err
	}
	if _, err := r.net("fetch", "--prune", r.remote, "+refs/airlock/requests/*:refs/airlock/requests/*"); err != nil {
		return nil, err
	}
	out, err := r.local("", "for-each-ref", "--format=%(refname) %(objectname)", strings.TrimSuffix(requestsRef, "/"))
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.HasPrefix(fields[0], requestsRef) {
			refs = append(refs, line)
		}
	}
	sort.Strings(refs)
	requests := make([]Request, 0, len(refs))
	for _, line := range refs {
		fields := strings.Fields(line)
		id := strings.TrimPrefix(fields[0], requestsRef)
		data, err := r.local("", "cat-file", "-p", fields[1])
		if err != nil {
			requests = append(requests, Request{ID: id, Action: ActionBroken})
			continue
		}
		var req Request
		if json.Unmarshal([]byte(data), &req) != nil {
			requests = append(requests, Request{ID: id, Action: ActionBroken})
			continue
		}
		if req.ID == "" {
			req.ID = id
		}
		requests = append(requests, req)
	}
	return requests, nil
}

func (r *RemoteStore) WriteResult(res *Result) error {
	if res.ID == "" {
		return errors.New("result has no id")
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	sha, err := r.local(string(data), "hash-object", "-w", "--stdin")
	if err != nil {
		return err
	}
	_, err = r.net("push", r.remote, sha+":"+resultsRef+res.ID)
	return err
}

func (r *RemoteStore) ReadResult(id string) (*Result, error) {
	if err := r.Ensure(); err != nil {
		return nil, err
	}
	ref := resultsRef + id
	if _, err := r.net("fetch", r.remote, "+"+ref+":"+ref); err != nil {
		return nil, os.ErrNotExist
	}
	data, err := r.local("", "cat-file", "-p", ref)
	if err != nil {
		return nil, os.ErrNotExist
	}
	var res Result
	if err := json.Unmarshal([]byte(data), &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (r *RemoteStore) MarkProcessed(id string) error {
	if err := r.Ensure(); err != nil {
		return err
	}
	_, err := r.net("push", r.remote, ":refs/airlock/requests/"+id)
	// Deleting an already-missing ref is not an error for our purposes.
	return err
}

func (r *RemoteStore) ListResults(limit int) ([]Result, error) {
	if err := r.Ensure(); err != nil {
		return nil, err
	}
	if _, err := r.net("fetch", "--prune", r.remote, "+refs/airlock/results/*:refs/airlock/results/*"); err != nil {
		return nil, err
	}
	out, err := r.local("", "for-each-ref", "--format=%(refname) %(objectname)", strings.TrimSuffix(resultsRef, "/"))
	if err != nil {
		return nil, err
	}
	type pair struct{ ref, sha string }
	var pairs []pair
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.HasPrefix(fields[0], resultsRef) {
			pairs = append(pairs, pair{fields[0], fields[1]})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].ref > pairs[j].ref })
	if limit <= 0 {
		limit = 20
	}
	results := make([]Result, 0, limit)
	for _, p := range pairs {
		if len(results) >= limit {
			break
		}
		data, err := r.local("", "cat-file", "-p", p.sha)
		if err != nil {
			continue
		}
		var res Result
		if json.Unmarshal([]byte(data), &res) != nil {
			continue
		}
		results = append(results, res)
	}
	return results, nil
}

func (r *RemoteStore) LogPath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "git-airlock", "worker.log")
}

func (r *RemoteStore) TailLog(lines int) (string, error) {
	path := r.LogPath()
	if path == "" {
		return "", nil
	}
	return tailFile(path, lines)
}

func (r *RemoteStore) Log(format string, args ...any) {
	path := r.LogPath()
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

func (r *RemoteStore) Wait(ctx context.Context, id string, interval time.Duration, onChange func(*Result)) (*Result, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var last string
	for {
		res, err := r.ReadResult(id)
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

func (r *RemoteStore) initCache() error {
	if err := os.MkdirAll(filepath.Dir(r.cache), 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "init", "--bare", r.cache)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git init --bare: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (r *RemoteStore) hash(req *Request) (string, error) {
	data, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	return r.local(string(data), "hash-object", "-w", "--stdin")
}

func (r *RemoteStore) local(stdin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", r.cache}, args...)...)
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

func (r *RemoteStore) net(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", r.cache}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
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
