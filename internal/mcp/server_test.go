package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/erikibolya/git-airlock/internal/config"
	"github.com/erikibolya/git-airlock/internal/queue"
)

func testServer(t *testing.T, input string) (string, *config.Config) {
	t.Helper()
	cfg := &config.Config{
		Repo: "owner/name", Provider: "github", Queue: t.TempDir(),
		Base: "main", Merge: "when-green", MergeStrategy: "merge",
	}
	q := queue.New(cfg.Queue)
	if err := q.Ensure(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	srv := &Server{Cfg: cfg, Q: q, Version: "test", In: strings.NewReader(input), Out: &out}
	if err := srv.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	return out.String(), cfg
}

func decodeResponses(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad response %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestInitializeAndToolsList(t *testing.T) {
	out, _ := testServer(t, strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, "\n")+"\n")
	rs := decodeResponses(t, out)
	if len(rs) != 2 {
		t.Fatalf("responses = %d", len(rs))
	}
	init, _ := rs[0]["result"].(map[string]any)
	if init["protocolVersion"] != ProtocolVersion {
		t.Fatalf("protocol = %v", init["protocolVersion"])
	}
	list, _ := rs[1]["result"].(map[string]any)
	tools, _ := list["tools"].([]any)
	if len(tools) < 4 {
		t.Fatalf("tools = %d", len(tools))
	}
	found := false
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if tool["name"] == "airlock_pr_open" {
			found = true
		}
	}
	if !found {
		t.Fatal("airlock_pr_open is missing")
	}
}

func TestStatusAndQueuedOpen(t *testing.T) {
	cfg := &config.Config{
		Repo: "owner/name", Provider: "github", Queue: t.TempDir(),
		Base: "main", Merge: "when-green", MergeStrategy: "squash",
	}
	q := queue.New(cfg.Queue)
	if err := q.Ensure(); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(&queue.Request{Action: queue.ActionStatus, Number: 3}); err != nil {
		t.Fatal(err)
	}

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"airlock_status","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"airlock_pr_open","arguments":{"head":"feature","wait_seconds":0}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	srv := &Server{Cfg: cfg, Q: q, Version: "test", In: strings.NewReader(input), Out: &out}
	if err := srv.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	rs := decodeResponses(t, out.String())
	if len(rs) != 2 {
		t.Fatalf("responses = %d", len(rs))
	}
	status, _ := rs[0]["result"].(map[string]any)
	content, _ := status["content"].([]any)
	text, _ := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, `"pending"`) || !strings.Contains(text, `"status"`) {
		t.Fatalf("status text = %s", text)
	}

	open, _ := rs[1]["result"].(map[string]any)
	content, _ = open["content"].([]any)
	text, _ = content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, `"status": "queued"`) {
		t.Fatalf("open text = %s", text)
	}
	pending, err := q.Pending()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, req := range pending {
		if req.Action == queue.ActionOpen && req.Head == "feature" && req.Strategy == "squash" {
			found = true
		}
	}
	if !found {
		t.Fatalf("open request missing from %+v", pending)
	}
}

func TestUnknownTool(t *testing.T) {
	out, _ := testServer(t, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"nope"}}`+"\n")
	rs := decodeResponses(t, out)
	res, _ := rs[0]["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("result = %+v", res)
	}
}
