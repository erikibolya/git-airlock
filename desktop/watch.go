package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/getlantern/systray"

	"github.com/erikibolya/git-airlock/internal/queue"
)

// watchResults polls the newest result of every repository and keeps the tray
// menu entry and tooltip on the latest event.
func (a *App) watchResults() {
	seen := map[string]string{}
	a.pollResults(seen)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if a.quitting.Load() {
			return
		}
		a.pollResults(seen)
	}
}

func (a *App) pollResults(seen map[string]string) {
	box := a.current.Load()
	if box == nil {
		return
	}
	for _, cfg := range box.cfgs {
		q, err := queue.Open(cfg.Queue, cfg.Mirror, cfg.QueueRemote)
		if err != nil {
			continue
		}
		results, err := q.ListResults(1)
		if err != nil || len(results) == 0 {
			continue
		}
		res := results[0]
		sig := res.ID + ":" + resultState(res)
		if sig == seen[cfg.Repo] {
			continue
		}
		seen[cfg.Repo] = sig
		if res.Pending {
			continue
		}
		body := res.Message
		if res.PR > 0 && !strings.Contains(body, fmt.Sprintf("#%d", res.PR)) {
			body = fmt.Sprintf("PR #%d: %s", res.PR, body)
		}
		if a.statusItem != nil {
			a.statusItem.SetTitle(cfg.Repo + ": " + statusLabel(res))
		}
		systray.SetTooltip(truncate("Git Airlock: "+body, 120))
	}
}

// statusLabel is the short tray entry for a result.
func statusLabel(res queue.Result) string {
	if res.PR > 0 {
		return fmt.Sprintf("PR #%d %s", res.PR, resultState(res))
	}
	switch {
	case res.Merged:
		return "merged"
	case res.State == "closed":
		return "closed"
	case res.Pending:
		return "waiting for checks"
	case res.OK:
		return "ok"
	default:
		return "failed"
	}
}

func resultState(res queue.Result) string {
	switch {
	case res.Pending:
		return "pending"
	case res.Merged:
		return "merged"
	case res.OK:
		return "ok"
	case res.State == "closed":
		return "closed"
	default:
		return "failed"
	}
}
