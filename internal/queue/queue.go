// Package queue implements the file-based airlock between the isolated host
// and the trusted worker.
package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Request actions.
const (
	ActionOpen   = "open"
	ActionStatus = "status"
	ActionMerge  = "merge"
	ActionSync   = "sync"
	ActionBroken = "__broken__"
)

// Request is written by the isolated host.
type Request struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	Head    string `json:"head,omitempty"`
	HeadSHA string `json:"head_sha,omitempty"`
	Base    string `json:"base,omitempty"`
	Title   string `json:"title,omitempty"`
	Body    string `json:"body,omitempty"`
	Merge   string `json:"merge,omitempty"`
	// Strategy is the provider merge method (merge, squash or rebase).
	Strategy string `json:"strategy,omitempty"`
	// DeleteBranch removes the source branch after a successful merge.
	DeleteBranch bool `json:"delete_branch,omitempty"`
	// Hold creates the PR as a draft so native auto-merge waits for a human review.
	Hold   bool `json:"hold,omitempty"`
	Number int  `json:"number,omitempty"`
	DryRun bool `json:"dry_run,omitempty"`
}

// Result is written by the worker.
type Result struct {
	ID        string `json:"id"`
	Action    string `json:"action"`
	OK        bool   `json:"ok"`
	Pending   bool   `json:"pending,omitempty"`
	Message   string `json:"message"`
	PR        int    `json:"pr,omitempty"`
	URL       string `json:"url,omitempty"`
	Head      string `json:"head,omitempty"`
	State     string `json:"state,omitempty"`
	Merged    bool   `json:"merged,omitempty"`
	Mergeable bool   `json:"mergeable,omitempty"`
	Checks    string `json:"checks,omitempty"`
}

// Store is the transport used by the CLI and the worker. The default is a
// shared directory; "git" stores requests/results as refs in the bare mirror.
type Store interface {
	Ensure() error
	Enqueue(*Request) (string, error)
	Pending() ([]Request, error)
	WriteResult(*Result) error
	ReadResult(string) (*Result, error)
	MarkProcessed(string) error
	Wait(ctx context.Context, id string, interval time.Duration, onChange func(*Result)) (*Result, error)
	ListResults(limit int) ([]Result, error)
	TailLog(lines int) (string, error)
	LogPath() string
	Log(format string, args ...any)
	Location(id string) string
}

// Open selects the queue transport. A queue value of "git" uses
// refs/airlock/* in the mirror; anything else is a shared directory.
func Open(queueCfg, mirror, queueRemote string) (Store, error) {
	if queueCfg == "git" {
		if queueRemote != "" {
			return NewRemote(queueRemote), nil
		}
		if mirror == "" {
			return nil, errors.New("queue=git requires mirror or queue_remote")
		}
		return NewGit(mirror), nil
	}
	if queueCfg == "" {
		return nil, errors.New("queue is empty")
	}
	return New(queueCfg), nil
}

// Queue is a directory-backed store.
type Queue struct {
	Dir string
}

// New returns a queue rooted at dir.
func New(dir string) *Queue {
	return &Queue{Dir: dir}
}

// Location is the result file path.
func (q *Queue) Location(id string) string {
	return filepath.Join(q.ResultsDir(), id+".json")
}

func (q *Queue) RequestsDir() string  { return filepath.Join(q.Dir, "requests") }
func (q *Queue) ResultsDir() string   { return filepath.Join(q.Dir, "results") }
func (q *Queue) ProcessedDir() string { return filepath.Join(q.Dir, "processed") }
func (q *Queue) TmpDir() string       { return filepath.Join(q.Dir, "tmp") }
func (q *Queue) LogDir() string       { return filepath.Join(q.Dir, "log") }

// Ensure creates the directory layout. The directories are world-writable
// because the isolated host may run as a different user/container root.
func (q *Queue) Ensure() error {
	for _, d := range []string{q.RequestsDir(), q.ResultsDir(), q.ProcessedDir(), q.TmpDir(), q.LogDir()} {
		if err := os.MkdirAll(d, 0o777); err != nil {
			return err
		}
		_ = os.Chmod(d, 0o777)
	}
	return nil
}

// Enqueue writes a request atomically and returns its id.
func (q *Queue) Enqueue(req *Request) (string, error) {
	if err := q.Ensure(); err != nil {
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
	tmp := filepath.Join(q.TmpDir(), req.ID+".json")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(q.RequestsDir(), req.ID+".json")); err != nil {
		return "", err
	}
	return req.ID, nil
}

// Pending returns queued requests, oldest first. Broken files become
// ActionBroken requests so the worker can report them instead of looping.
func (q *Queue) Pending() ([]Request, error) {
	entries, err := os.ReadDir(q.RequestsDir())
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := make([]Request, 0, len(names))
	for _, name := range names {
		path := filepath.Join(q.RequestsDir(), name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var req Request
		if err := json.Unmarshal(data, &req); err != nil {
			req = Request{ID: strings.TrimSuffix(name, ".json"), Action: ActionBroken}
		}
		if req.ID == "" {
			req.ID = strings.TrimSuffix(name, ".json")
		}
		out = append(out, req)
	}
	return out, nil
}

// WriteResult stores a result atomically.
func (q *Queue) WriteResult(res *Result) error {
	if err := q.Ensure(); err != nil {
		return err
	}
	if res.ID == "" {
		return errors.New("result has no id")
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := filepath.Join(q.TmpDir(), res.ID+".result.json")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(q.ResultsDir(), res.ID+".json"))
}

// ReadResult returns a result or os.ErrNotExist.
func (q *Queue) ReadResult(id string) (*Result, error) {
	data, err := os.ReadFile(filepath.Join(q.ResultsDir(), id+".json"))
	if err != nil {
		return nil, err
	}
	var res Result
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// ListResults returns the newest results first.
func (q *Queue) ListResults(limit int) ([]Result, error) {
	entries, err := os.ReadDir(q.ResultsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if limit <= 0 {
		limit = 20
	}
	out := make([]Result, 0, limit)
	for _, name := range names {
		if len(out) >= limit {
			break
		}
		data, err := os.ReadFile(filepath.Join(q.ResultsDir(), name))
		if err != nil {
			continue
		}
		var res Result
		if json.Unmarshal(data, &res) != nil {
			continue
		}
		out = append(out, res)
	}
	return out, nil
}

// LogPath is the worker log file.
func (q *Queue) LogPath() string {
	return filepath.Join(q.LogDir(), "worker.log")
}

// TailLog returns the last lines of the worker log.
func (q *Queue) TailLog(lines int) (string, error) {
	return tailFile(q.LogPath(), lines)
}

func tailFile(path string, lines int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	if lines <= 0 {
		lines = 200
	}
	parts := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n") + "\n", nil
}

// MarkProcessed moves a final request out of the queue.
func (q *Queue) MarkProcessed(id string) error {
	src := filepath.Join(q.RequestsDir(), id+".json")
	dst := filepath.Join(q.ProcessedDir(), id+".json")
	if err := os.Rename(src, dst); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}

// Wait polls for a final result. pending results keep it waiting;
// onChange is called whenever the result changes.
func (q *Queue) Wait(ctx context.Context, id string, interval time.Duration, onChange func(*Result)) (*Result, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var last string
	for {
		res, err := q.ReadResult(id)
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
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Log appends a line to log/worker.log, rotating at 1 MiB.
func (q *Queue) Log(format string, args ...any) {
	_ = q.Ensure()
	path := filepath.Join(q.LogDir(), "worker.log")
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

// NewID returns a sortable unique request id.
func NewID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}
