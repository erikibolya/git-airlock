package gitea

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/erikibolya/git-airlock/internal/provider"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"number": 5, "html_url": "https://gitea/o/r/pulls/5", "state": "open",
				"head": map[string]any{"ref": "feature", "sha": "sha1"},
				"base": map[string]any{"ref": "main"},
			}})
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 6, "html_url": "https://gitea/o/r/pulls/6", "state": "open",
				"head": map[string]any{"ref": "feature", "sha": "sha2"},
				"base": map[string]any{"ref": "main"},
			})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/v1/repos/o/r/pulls/5", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number": 5, "html_url": "https://gitea/o/r/pulls/5", "state": "open",
			"mergeable": true, "merged": false,
			"head": map[string]any{"ref": "feature", "sha": "sha1"},
			"base": map[string]any{"ref": "main"},
		})
	})
	mux.HandleFunc("/api/v1/repos/o/r/pulls/5/merge", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/v1/repos/o/r/commits/sha1/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"state": "failure",
			"statuses": []map[string]string{
				{"status": "failure", "context": "Build / build (push)"},
				{"status": "success", "context": "Build / build (pull_request)"},
				{"status": "success", "context": "After PR / build (pull_request)"},
			},
		})
	})
	return httptest.NewServer(mux)
}

func TestFindCreateGetMerge(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	p := New(srv.URL+"/api/v1", "o/r", "token", srv.Client())

	pr, err := p.FindOpenPR(context.Background(), "feature", "main")
	if err != nil || pr == nil || pr.Number != 5 {
		t.Fatalf("find: %+v %v", pr, err)
	}
	all, err := p.FindPR(context.Background(), "feature", "main")
	if err != nil || all == nil || all.Number != 5 {
		t.Fatalf("find all: %+v %v", all, err)
	}
	created, err := p.CreatePR(context.Background(), provider.CreateRequest{Head: "feature", Base: "main", Title: "t"})
	if err != nil || created.Number != 6 {
		t.Fatalf("create: %+v %v", created, err)
	}
	got, err := p.GetPR(context.Background(), 5)
	if err != nil || !got.Mergeable || got.Merged {
		t.Fatalf("get: %+v %v", got, err)
	}
	if err := p.Merge(context.Background(), 5, "merge"); err != nil {
		t.Fatalf("merge: %v", err)
	}
}

func TestChecksIgnorePushCancellation(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	p := New(srv.URL+"/api/v1", "o/r", "token", srv.Client())
	pr := &provider.PR{Number: 5, HeadSHA: "sha1"}
	checks, err := p.Checks(context.Background(), pr)
	if err != nil {
		t.Fatal(err)
	}
	if checks != provider.ChecksSuccess {
		t.Fatalf("expected success, got %s", checks)
	}
}

func TestCreateConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()
	p := New(srv.URL, "o/r", "token", srv.Client())
	_, err := p.CreatePR(context.Background(), provider.CreateRequest{Head: "h", Base: "b", Title: "t"})
	if !errors.Is(err, provider.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestMergeStrategyBody(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = body["Do"]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	p := New(srv.URL+"/api/v1", "o/r", "token", srv.Client())
	if err := p.Merge(context.Background(), 5, "squash"); err != nil {
		t.Fatal(err)
	}
	if got != "squash" {
		t.Fatalf("Do = %q, want squash", got)
	}
	if err := p.Merge(context.Background(), 5, "bogus"); err == nil {
		t.Fatal("accepted an unknown strategy")
	}
}

func TestDeleteBranch(t *testing.T) {
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	p := New(srv.URL+"/api/v1", "o/r", "token", srv.Client())
	if err := p.DeleteBranch(context.Background(), "feature/x"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete || !strings.Contains(path, "feature") {
		t.Fatalf("method=%s path=%s", method, path)
	}
}

// A full first page must trigger a second fetch: a branch's older PR can be on page 2,
// and missing it would make auto-PR propose a duplicate.
func TestListPRsFetchesSecondPage(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		n := 50
		if page == "2" {
			n = 1
		}
		items := make([]map[string]any, 0, n)
		for i := 0; i < n; i++ {
			items = append(items, map[string]any{
				"number": i + 1, "state": "closed",
				"head": map[string]any{"ref": "feature", "sha": "sha1"},
				"base": map[string]any{"ref": "main"},
			})
		}
		_ = json.NewEncoder(w).Encode(items)
	}))
	defer srv.Close()
	p := New(srv.URL+"/api/v1", "o/r", "token", srv.Client())

	prs, err := p.ListPRs(context.Background(), "main", "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 51 {
		t.Fatalf("len = %d, want 51 (50 + 1)", len(prs))
	}
	if len(pages) != 2 || pages[0] != "1" || pages[1] != "2" {
		t.Fatalf("pages fetched = %v, want [1 2]", pages)
	}
}
