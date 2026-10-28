// Package worker executes queued airlock requests against a provider.
package worker

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/erikibolya/git-airlock/internal/config"
	"github.com/erikibolya/git-airlock/internal/gitutil"
	"github.com/erikibolya/git-airlock/internal/proc"
	"github.com/erikibolya/git-airlock/internal/provider"
	"github.com/erikibolya/git-airlock/internal/queue"
)

// Worker processes the shared queue.
type Worker struct {
	Cfg *config.Config
	P   provider.Provider
	Q   queue.Store
}

// New builds a worker for one repository config.
func New(cfg *config.Config, p provider.Provider) (*Worker, error) {
	q, err := queue.Open(cfg.Queue, cfg.Mirror, cfg.QueueRemote)
	if err != nil {
		return nil, err
	}
	return &Worker{Cfg: cfg, P: p, Q: q}, nil
}

// Run processes all pending requests once and returns how many were handled.
func (w *Worker) Run(ctx context.Context) (int, error) {
	if err := w.Q.Ensure(); err != nil {
		return 0, err
	}
	lock := flock.New(lockPath(w.Cfg.Queue))
	locked, err := lock.TryLock()
	if err != nil {
		return 0, fmt.Errorf("lock: %w", err)
	}
	if !locked {
		w.Q.Log("another worker is running, skipping")
		return 0, nil
	}
	defer func() { _ = lock.Unlock() }()

	requests, err := w.Q.Pending()
	if err != nil {
		return 0, err
	}
	for _, req := range requests {
		reqCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		res := w.handle(reqCtx, req)
		cancel()
		if err := w.Q.WriteResult(&res); err != nil {
			w.Q.Log("%s: cannot write result: %v", req.ID, err)
			continue
		}
		if !res.Pending {
			if err := w.Q.MarkProcessed(req.ID); err != nil {
				w.Q.Log("%s: cannot mark processed: %v", req.ID, err)
			}
		}
		w.Q.Log("%s %s ok=%v pending=%v: %s", req.ID, req.Action, res.OK, res.Pending, res.Message)
	}
	return len(requests), nil
}

// AutoOpen queues an open request for every local branch that is ahead of the
// base branch and has no open pull request yet. The normal queue pipeline
// creates the PR and merges it according to merge (usually when-green).
func (w *Worker) AutoOpen(ctx context.Context) (int, error) {
	if !w.Cfg.AutoPR {
		return 0, nil
	}
	baseRef := "refs/remotes/" + w.Cfg.Remote + "/" + w.Cfg.Base
	if !gitutil.RefExists(w.Cfg.Mirror, baseRef) {
		baseRef = "refs/heads/" + w.Cfg.Base
	}
	branches, err := gitutil.BranchesAhead(ctx, w.Cfg.Mirror, baseRef)
	if err != nil {
		return 0, fmt.Errorf("scan branches: %w", err)
	}
	pending, err := w.Q.Pending()
	if err != nil {
		return 0, err
	}
	queuedOpen := map[string]bool{}
	for _, req := range pending {
		if req.Action == queue.ActionOpen {
			queuedOpen[req.Head] = true
		}
	}
	merge := w.Cfg.Merge
	if merge == "" {
		merge = "when-green"
	}
	queued := 0
	for _, br := range branches {
		if w.ignoredBranch(br.Name) || queuedOpen[br.Name] {
			continue
		}
		open, err := w.P.FindOpenPR(ctx, br.Name, w.Cfg.Base)
		if err != nil {
			w.Q.Log("auto-pr %s: %v", br.Name, err)
			continue
		}
		if open != nil {
			continue
		}
		latest, err := w.P.FindPR(ctx, br.Name, w.Cfg.Base)
		if err != nil {
			w.Q.Log("auto-pr %s: %v", br.Name, err)
			continue
		}
		// A merged (e.g. squashed) or explicitly closed PR for this exact
		// commit means there is nothing new to propose. A newer commit is a
		// new intent and gets a fresh PR.
		if latest != nil && latest.HeadSHA == br.SHA && (latest.Merged || latest.State == "closed") {
			continue
		}
		req := &queue.Request{
			Action:  queue.ActionOpen,
			Head:    br.Name,
			HeadSHA: br.SHA,
			Base:    w.Cfg.Base,
			Merge:   merge,
			Title:   br.Subject,
			Body:    "Opened automatically by Git Airlock.",
		}
		id, err := w.Q.Enqueue(req)
		if err != nil {
			w.Q.Log("auto-pr %s: enqueue: %v", br.Name, err)
			continue
		}
		w.Q.Log("auto-pr %s: queued %s (merge=%s)", br.Name, id, merge)
		queued++
	}
	return queued, nil
}

func (w *Worker) ignoredBranch(name string) bool {
	for _, pattern := range w.Cfg.AutoPRIgnore {
		if ok, _ := filepath.Match(pattern, name); ok {
			return true
		}
	}
	return false
}

func lockPath(queueDir string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(queueDir))
	return filepath.Join(os.TempDir(), fmt.Sprintf("git-airlock-%08x.lock", h.Sum32()))
}

func (w *Worker) handle(ctx context.Context, req queue.Request) queue.Result {
	res := queue.Result{ID: req.ID, Action: req.Action}

	if req.Action == queue.ActionBroken {
		res.Message = "invalid request JSON"
		return res
	}
	switch req.Action {
	case queue.ActionOpen, queue.ActionStatus, queue.ActionMerge:
	default:
		res.Message = fmt.Sprintf("unknown action %q", req.Action)
		return res
	}

	head := req.Head
	base := req.Base
	if base == "" {
		base = w.Cfg.Base
	}
	merge := req.Merge
	if merge == "" {
		merge = w.Cfg.Merge
	}
	if req.Action == queue.ActionMerge && req.Merge == "" {
		merge = "now"
	}

	switch req.Action {
	case queue.ActionOpen:
		w.handleOpen(ctx, &res, head, base, req, merge)
	case queue.ActionStatus:
		w.handleStatus(ctx, &res, req.Number)
	case queue.ActionMerge:
		w.handleMerge(ctx, &res, req.Number, merge)
	}
	return res
}

func (w *Worker) handleOpen(ctx context.Context, res *queue.Result, head, base string, req queue.Request, merge string) {
	if head == "" {
		fail(res, errors.New("open: head branch is empty"))
		return
	}
	if !validBranch(head) || !validBranch(base) {
		fail(res, errors.New("open: invalid branch name"))
		return
	}
	if head == base {
		fail(res, fmt.Errorf("open: head and base are both %q", base))
		return
	}
	if merge != "never" && merge != "when-green" && merge != "now" {
		fail(res, fmt.Errorf("open: invalid merge mode %q", merge))
		return
	}
	if req.HeadSHA != "" && w.baseContains(req.HeadSHA, base) {
		applyPR(res, &provider.PR{State: "merged", Merged: true, HeadSHA: req.HeadSHA})
		res.OK = true
		res.Message = fmt.Sprintf("commit %.8s is already on %s; nothing to merge", req.HeadSHA, base)
		return
	}

	pr, err := w.P.FindOpenPR(ctx, head, base)
	if err != nil {
		fail(res, err)
		return
	}
	if pr == nil {
		// No open PR. If the newest PR for this branch pair is already merged
		// (or was closed), the request was already fulfilled; do not create a
		// duplicate. A new commit on the same branch carries a new HeadSHA and
		// is allowed to open a fresh PR.
		latest, err := w.P.FindPR(ctx, head, base)
		if err != nil {
			fail(res, err)
			return
		}
		if latest != nil {
			switch {
			case latest.Merged && (req.HeadSHA == "" || latest.HeadSHA == req.HeadSHA):
				applyPR(res, latest)
				res.OK = true
				res.Message = fmt.Sprintf("PR #%d already merged", latest.Number)
				return
			case latest.State == "closed" && !latest.Merged && (req.HeadSHA == "" || latest.HeadSHA == req.HeadSHA):
				applyPR(res, latest)
				res.Message = fmt.Sprintf("PR #%d closed without merge", latest.Number)
				return
			case latest.State == "open":
				pr = latest
			}
		}
	}
	if pr == nil && req.DryRun {
		res.OK = true
		res.Message = fmt.Sprintf("dry-run: would create PR %s -> %s (merge=%s)", head, base, merge)
		return
	}
	if pr == nil {
		title := strings.TrimSpace(req.Title)
		if title == "" {
			title = "Automated PR: " + head + " -> " + base
		}
		pr, err = w.P.CreatePR(ctx, provider.CreateRequest{
			Head:  head,
			Base:  base,
			Title: title,
			Body:  req.Body,
		})
		if errors.Is(err, provider.ErrAlreadyExists) {
			pr, err = w.P.FindOpenPR(ctx, head, base)
		}
		if err != nil {
			fail(res, err)
			return
		}
	}
	if pr == nil {
		fail(res, errors.New("open: PR not found after create"))
		return
	}
	if err := w.refresh(ctx, res, pr.Number); err != nil {
		fail(res, err)
		return
	}
	if res.Merged {
		res.OK = true
		res.Message = fmt.Sprintf("PR #%d already merged", pr.Number)
		return
	}
	if res.State == "closed" {
		fail(res, fmt.Errorf("PR #%d closed without merge", pr.Number))
		return
	}
	if merge == "never" {
		res.OK = true
		res.Message = fmt.Sprintf("PR #%d open", pr.Number)
		return
	}
	if merge == "when-green" {
		if res.Checks == string(provider.ChecksFailure) {
			fail(res, fmt.Errorf("PR #%d checks failed", pr.Number))
			return
		}
		if res.Checks != string(provider.ChecksSuccess) {
			res.Pending = true
			res.Message = fmt.Sprintf("PR #%d open; waiting for checks (%s)", pr.Number, checksLabel(res.Checks))
			return
		}
	}
	w.merge(ctx, res, pr.Number)
}

func (w *Worker) handleStatus(ctx context.Context, res *queue.Result, number int) {
	if number <= 0 {
		fail(res, errors.New("status: PR number is required"))
		return
	}
	if err := w.refresh(ctx, res, number); err != nil {
		fail(res, err)
		return
	}
	res.OK = true
	res.Message = fmt.Sprintf("PR #%d state=%s merged=%v checks=%s", number, res.State, res.Merged, checksLabel(res.Checks))
}

func (w *Worker) handleMerge(ctx context.Context, res *queue.Result, number int, merge string) {
	if number <= 0 {
		fail(res, errors.New("merge: PR number is required"))
		return
	}
	if merge != "now" && merge != "when-green" {
		fail(res, fmt.Errorf("merge: invalid merge mode %q", merge))
		return
	}
	if err := w.refresh(ctx, res, number); err != nil {
		fail(res, err)
		return
	}
	if res.Merged {
		res.OK = true
		res.Message = fmt.Sprintf("PR #%d already merged", number)
		return
	}
	if res.State == "closed" {
		fail(res, fmt.Errorf("PR #%d closed without merge", number))
		return
	}
	if merge == "when-green" {
		if res.Checks == string(provider.ChecksFailure) {
			fail(res, fmt.Errorf("PR #%d checks failed", number))
			return
		}
		if res.Checks != string(provider.ChecksSuccess) {
			res.Pending = true
			res.Message = fmt.Sprintf("PR #%d waiting for checks (%s)", number, checksLabel(res.Checks))
			return
		}
	}
	w.merge(ctx, res, number)
}

func (w *Worker) merge(ctx context.Context, res *queue.Result, number int) {
	if err := w.P.Merge(ctx, number); err != nil {
		// A concurrent merge may have won the race; re-read before failing.
		if pr, getErr := w.P.GetPR(ctx, number); getErr == nil && pr.Merged {
			applyPR(res, pr)
			res.OK = true
			res.Message = fmt.Sprintf("PR #%d merged", number)
			return
		}
		fail(res, err)
		return
	}
	pr, err := w.P.GetPR(ctx, number)
	if err != nil {
		fail(res, err)
		return
	}
	applyPR(res, pr)
	if pr.Merged {
		res.OK = true
		res.Message = fmt.Sprintf("PR #%d merged", number)
		return
	}
	res.Pending = true
	res.Message = fmt.Sprintf("PR #%d merge requested; not merged yet", number)
}

func (w *Worker) refresh(ctx context.Context, res *queue.Result, number int) error {
	pr, err := w.P.GetPR(ctx, number)
	if err != nil {
		return err
	}
	applyPR(res, pr)
	checks, err := w.P.Checks(ctx, pr)
	if err != nil {
		return err
	}
	res.Checks = string(checks)
	return nil
}

// baseContains reports whether sha is already an ancestor of the remote base
// branch in the mirror. It is the cheapest way to tell "already merged" from
// "needs a new PR" without another API round trip.
func (w *Worker) baseContains(sha, base string) bool {
	if sha == "" || base == "" || w.Cfg.Mirror == "" || w.Cfg.Remote == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ref := "refs/remotes/" + w.Cfg.Remote + "/" + base
	return proc.CommandContext(ctx, "git", "-C", w.Cfg.Mirror, "merge-base", "--is-ancestor", sha, ref).Run() == nil
}

func applyPR(res *queue.Result, pr *provider.PR) {
	res.PR = pr.Number
	res.URL = pr.URL
	res.State = pr.State
	res.Merged = pr.Merged
	res.Mergeable = pr.Mergeable
}

func fail(res *queue.Result, err error) {
	res.OK = false
	if provider.Retryable(err) {
		res.Pending = true
		res.Message = "will retry: " + err.Error()
		return
	}
	res.Message = err.Error()
}

func checksLabel(c string) string {
	if c == "" {
		return "none"
	}
	return c
}

func validBranch(s string) bool {
	if s == "" || len(s) > 200 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '_' || r == '/' || r == '-' {
			continue
		}
		return false
	}
	return !strings.HasPrefix(s, "-") && !strings.Contains(s, "..")
}
