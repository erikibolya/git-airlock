package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/erikibolya/git-airlock/internal/provider"
)

func TestFindCreateGetMerge(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"number": 7, "html_url": "https://github/o/r/pull/7", "state": "open",
				"mergeable": true,
				"head":      map[string]any{"ref": "feature", "sha": "sha1"},
				"base":      map[string]any{"ref": "main"},
			}})
		case http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 8, "html_url": "https://github/o/r/pull/8", "state": "open",
				"head": map[string]any{"ref": "feature", "sha": "sha2"},
				"base": map[string]any{"ref": "main"},
			})
		}
	})
	mux.HandleFunc("/repos/o/r/pulls/7/merge", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	p := New(srv.URL, "o/r", "token", srv.Client())

	pr, err := p.FindOpenPR(context.Background(), "feature", "main")
	if err != nil || pr == nil || pr.Number != 7 {
		t.Fatalf("find: %+v %v", pr, err)
	}
	all, err := p.FindPR(context.Background(), "feature", "main")
	if err != nil || all == nil || all.Number != 7 {
		t.Fatalf("find all: %+v %v", all, err)
	}
	created, err := p.CreatePR(context.Background(), provider.CreateRequest{Head: "feature", Base: "main", Title: "t"})
	if err != nil || created.Number != 8 {
		t.Fatalf("create: %+v %v", created, err)
	}
	if err := p.Merge(context.Background(), 7, "merge"); err != nil {
		t.Fatalf("merge: %v", err)
	}
}

func TestChecks(t *testing.T) {
	cases := []struct {
		name     string
		runs     []map[string]string
		combined string
		total    int
		want     provider.Checks
	}{
		{
			name:     "success",
			runs:     []map[string]string{{"status": "completed", "conclusion": "success"}},
			combined: "success", total: 1, want: provider.ChecksSuccess,
		},
		{
			name:     "failure",
			runs:     []map[string]string{{"status": "completed", "conclusion": "failure"}},
			combined: "success", total: 1, want: provider.ChecksFailure,
		},
		{
			name:     "pending",
			runs:     []map[string]string{{"status": "in_progress", "conclusion": ""}},
			combined: "pending", total: 1, want: provider.ChecksPending,
		},
		{
			name:     "none",
			runs:     nil,
			combined: "", total: 0, want: provider.ChecksNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/repos/o/r/commits/sha1/check-runs", func(w http.ResponseWriter, r *http.Request) {
				if tc.runs == nil {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"check_runs": tc.runs})
			})
			mux.HandleFunc("/repos/o/r/commits/sha1/status", func(w http.ResponseWriter, r *http.Request) {
				if tc.total == 0 {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"state": tc.combined, "total_count": tc.total})
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			p := New(srv.URL, "o/r", "token", srv.Client())
			got, err := p.Checks(context.Background(), &provider.PR{Number: 1, HeadSHA: "sha1"})
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestCreateConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
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
		got = body["merge_method"]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	p := New(srv.URL, "o/r", "token", srv.Client())
	if err := p.Merge(context.Background(), 7, "rebase"); err != nil {
		t.Fatal(err)
	}
	if got != "rebase" {
		t.Fatalf("merge_method = %q, want rebase", got)
	}
}
