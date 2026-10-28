// Package mcp exposes Git Airlock as a Model Context Protocol server over
// stdio. It only talks to the queue; the worker on the trusted workstation
// performs the provider calls.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/erikibolya/git-airlock/internal/config"
	"github.com/erikibolya/git-airlock/internal/proc"
	"github.com/erikibolya/git-airlock/internal/provider"
	"github.com/erikibolya/git-airlock/internal/queue"
)

// ProtocolVersion is the MCP revision this server speaks.
const ProtocolVersion = "2024-11-05"

const defaultWait = 55

// Server is a stdio MCP server. In and Out default to the process stdio.
type Server struct {
	Cfg     *config.Config
	Q       queue.Store
	Version string
	In      io.Reader
	Out     io.Writer
	// Log writes diagnostics; it must never write to Out.
	Log func(format string, args ...any)
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callResult struct {
	Content []textContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// Run reads newline-delimited JSON-RPC requests until In is closed.
func (s *Server) Run(ctx context.Context) error {
	if s.In == nil || s.Out == nil {
		return errors.New("mcp: In and Out are required")
	}
	scanner := bufio.NewScanner(s.In)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.write(response{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error: " + err.Error()}})
			continue
		}
		if len(req.ID) == 0 {
			// Notifications (notifications/initialized, cancelled, ...) need no reply.
			continue
		}
		s.write(s.handle(ctx, req))
	}
	return scanner.Err()
}

func (s *Server) write(resp response) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintln(s.Out, string(data))
}

func (s *Server) handle(ctx context.Context, req request) response {
	resp := response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "git-airlock", "version": s.Version},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": toolDefinitions()}
	case "tools/call":
		var p callParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
			return resp
		}
		resp.Result = s.callTool(ctx, p)
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp
}

// ---------- tools ----------

func toolDefinitions() []map[string]any {
	wait := map[string]any{"type": "integer", "minimum": 0, "maximum": 600, "description": "seconds to wait for the worker (default 55, 0 = return immediately)"}
	strategy := map[string]any{"type": "string", "enum": []string{"merge", "squash", "rebase"}}
	merge := map[string]any{"type": "string", "enum": []string{"never", "when-green", "now"}}
	return []map[string]any{
		{
			"name":        "airlock_status",
			"description": "Pending requests, recent results and the worker log for this repository.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "airlock_sync",
			"description": "Ask the worker to sync the mirror with the private remote now.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"wait_seconds": wait},
			},
		},
		{
			"name":        "airlock_pr_open",
			"description": "Open a pull request for a branch pushed to the mirror and (by default) merge it when checks are green. Returns the queued request id and its latest result.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"head":          map[string]any{"type": "string", "description": "source branch in the mirror"},
					"base":          map[string]any{"type": "string", "description": "target branch (default from config)"},
					"title":         map[string]any{"type": "string"},
					"body":          map[string]any{"type": "string"},
					"merge":         merge,
					"strategy":      strategy,
					"delete_branch": map[string]any{"type": "boolean"},
					"dry_run":       map[string]any{"type": "boolean"},
					"wait_seconds":  wait,
				},
				"required": []string{"head"},
			},
		},
		{
			"name":        "airlock_pr_status",
			"description": "Read the latest result for a queued request id, or ask the worker to check a PR number.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"request_id":   map[string]any{"type": "string"},
					"number":       map[string]any{"type": "integer", "minimum": 1},
					"wait_seconds": wait,
				},
			},
		},
		{
			"name":        "airlock_pr_merge",
			"description": "Merge a pull request, optionally only when checks are green.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"number":        map[string]any{"type": "integer", "minimum": 1},
					"when_green":    map[string]any{"type": "boolean", "description": "wait for green checks before merging"},
					"strategy":      strategy,
					"delete_branch": map[string]any{"type": "boolean"},
					"wait_seconds":  wait,
				},
				"required": []string{"number"},
			},
		},
	}
}

func (s *Server) callTool(ctx context.Context, p callParams) callResult {
	var args map[string]any
	if len(p.Arguments) > 0 && string(p.Arguments) != "null" {
		if err := json.Unmarshal(p.Arguments, &args); err != nil {
			return failResult("invalid arguments: " + err.Error())
		}
	}
	if args == nil {
		args = map[string]any{}
	}
	var (
		text string
		err  error
	)
	switch p.Name {
	case "airlock_status":
		text, err = s.toolStatus()
	case "airlock_sync":
		text, err = s.toolSync(ctx, args)
	case "airlock_pr_open":
		text, err = s.toolOpen(ctx, args)
	case "airlock_pr_status":
		text, err = s.toolStatusPR(ctx, args)
	case "airlock_pr_merge":
		text, err = s.toolMerge(ctx, args)
	default:
		return failResult("unknown tool: " + p.Name)
	}
	if err != nil {
		return failResult(err.Error())
	}
	return callResult{Content: []textContent{{Type: "text", Text: text}}}
}

func failResult(msg string) callResult {
	return callResult{Content: []textContent{{Type: "text", Text: msg}}, IsError: true}
}

func (s *Server) toolStatus() (string, error) {
	pending, err := s.Q.Pending()
	if err != nil {
		return "", err
	}
	results, err := s.Q.ListResults(20)
	if err != nil {
		return "", err
	}
	logs, _ := s.Q.TailLog(20)
	body := map[string]any{
		"repo":     s.Cfg.Repo,
		"provider": s.Cfg.Provider,
		"mirror":   s.Cfg.Mirror,
		"pending":  pending,
		"results":  results,
	}
	if logs != "" {
		body["log"] = logs
	}
	return marshalText(body)
}

func (s *Server) toolSync(ctx context.Context, args map[string]any) (string, error) {
	wait := waitSeconds(args, defaultWait)
	id, err := s.Q.Enqueue(&queue.Request{Action: queue.ActionSync})
	if err != nil {
		return "", err
	}
	return s.finish(ctx, id, wait)
}

func (s *Server) toolOpen(ctx context.Context, args map[string]any) (string, error) {
	head := stringArg(args, "head")
	if head == "" {
		return "", errors.New("head is required")
	}
	base := stringArg(args, "base")
	if base == "" {
		base = s.Cfg.Base
	}
	if head == base {
		return "", fmt.Errorf("head and base are both %q", base)
	}
	merge := stringArg(args, "merge")
	if merge == "" {
		merge = s.Cfg.Merge
	}
	switch merge {
	case "never", "when-green", "now":
	default:
		return "", fmt.Errorf("merge must be never, when-green or now, got %q", merge)
	}
	strategy := stringArg(args, "strategy")
	if strategy == "" {
		strategy = s.Cfg.MergeStrategy
	}
	if _, ok := provider.NormalizeMergeStrategy(strategy); !ok {
		return "", fmt.Errorf("strategy must be merge, squash or rebase, got %q", strategy)
	}
	headSHA := ""
	if out, err := proc.Command("git", "-C", s.Cfg.Mirror, "rev-parse", "refs/heads/"+head).Output(); err == nil {
		headSHA = strings.TrimSpace(string(out))
	}
	req := &queue.Request{
		Action:       queue.ActionOpen,
		Head:         head,
		HeadSHA:      headSHA,
		Base:         base,
		Title:        stringArg(args, "title"),
		Body:         stringArg(args, "body"),
		Merge:        merge,
		Strategy:     strategy,
		DeleteBranch: boolArg(args, "delete_branch") || s.Cfg.DeleteBranch,
		DryRun:       boolArg(args, "dry_run"),
	}
	id, err := s.Q.Enqueue(req)
	if err != nil {
		return "", err
	}
	return s.finish(ctx, id, waitSeconds(args, defaultWait))
}

func (s *Server) toolStatusPR(ctx context.Context, args map[string]any) (string, error) {
	if id := stringArg(args, "request_id"); id != "" {
		wait := waitSeconds(args, 0)
		return s.finish(ctx, id, wait)
	}
	number := intArg(args, "number")
	if number <= 0 {
		return "", errors.New("request_id or number is required")
	}
	id, err := s.Q.Enqueue(&queue.Request{Action: queue.ActionStatus, Number: number})
	if err != nil {
		return "", err
	}
	return s.finish(ctx, id, waitSeconds(args, defaultWait))
}

func (s *Server) toolMerge(ctx context.Context, args map[string]any) (string, error) {
	number := intArg(args, "number")
	if number <= 0 {
		return "", errors.New("number is required")
	}
	merge := "now"
	if boolArg(args, "when_green") {
		merge = "when-green"
	}
	strategy := stringArg(args, "strategy")
	if strategy == "" {
		strategy = s.Cfg.MergeStrategy
	}
	if _, ok := provider.NormalizeMergeStrategy(strategy); !ok {
		return "", fmt.Errorf("strategy must be merge, squash or rebase, got %q", strategy)
	}
	id, err := s.Q.Enqueue(&queue.Request{
		Action:       queue.ActionMerge,
		Number:       number,
		Merge:        merge,
		Strategy:     strategy,
		DeleteBranch: boolArg(args, "delete_branch") || s.Cfg.DeleteBranch,
	})
	if err != nil {
		return "", err
	}
	return s.finish(ctx, id, waitSeconds(args, defaultWait))
}

// finish waits for the worker and renders the result. A timeout is not an
// error: the request simply stays queued.
func (s *Server) finish(ctx context.Context, id string, wait int) (string, error) {
	body := map[string]any{"request_id": id, "location": s.Q.Location(id)}
	if wait <= 0 {
		body["status"] = "queued"
		return marshalText(body)
	}
	if res, err := s.Q.ReadResult(id); err == nil && (res.OK || !res.Pending) {
		body["result"] = res
		body["status"] = resultStatus(res)
		return marshalText(body)
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(wait)*time.Second)
	defer cancel()
	res, err := s.Q.Wait(waitCtx, id, 2*time.Second, nil)
	if err != nil {
		body["status"] = "queued"
		body["message"] = "still queued; the worker will process it"
		return marshalText(body)
	}
	body["result"] = res
	body["status"] = resultStatus(res)
	return marshalText(body)
}

func resultStatus(res *queue.Result) string {
	switch {
	case res == nil:
		return "unknown"
	case res.Pending:
		return "pending"
	case res.OK:
		return "done"
	default:
		return "failed"
	}
}

func marshalText(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func stringArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func boolArg(args map[string]any, key string) bool {
	v, _ := args[key].(bool)
	return v
}

func intArg(args map[string]any, key string) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	return 0
}

func waitSeconds(args map[string]any, def int) int {
	n := intArg(args, "wait_seconds")
	if n <= 0 {
		if _, ok := args["wait_seconds"]; ok {
			return 0
		}
		return def
	}
	if n > 600 {
		return 600
	}
	return n
}
