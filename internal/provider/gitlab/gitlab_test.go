package gitlab

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

func TestFindCreateGetMerge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/merge_requests/5/merge"):
			if r.Method != http.MethodPut {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(p, "/merge_requests/5"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"iid": 5, "web_url": "https://gitlab/o/r/-/merge_requests/5", "state": "opened",
				"merge_status": "can_be_merged", "sha": "sha1",
				"source_branch": "feature", "target_branch": "main",
			})
		case strings.HasSuffix(p, "/merge_requests") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"iid": 5, "web_url": "https://gitlab/o/r/-/merge_requests/5", "state": "opened",
				"merge_status": "can_be_merged", "sha": "sha1",
				"source_branch": "feature", "target_branch": "main",
			}})
		case strings.HasSuffix(p, "/merge_requests") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"iid": 6, "web_url": "https://gitlab/o/r/-/merge_requests/6", "state": "opened",
				"merge_status": "can_be_merged", "sha": "sha2",
				"source_branch": "feature", "target_branch": "main",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	p := New(srv.URL+"/api/v4", "o/r", "token", srv.Client())

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
	if err != nil || got.Number != 5 || !got.Mergeable {
		t.Fatalf("get: %+v %v", got, err)
	}
	if err := p.Merge(context.Background(), 5, "merge"); err != nil {
		t.Fatalf("merge: %v", err)
	}
}

func TestChecks(t *testing.T) {
	cases := map[string]provider.Checks{
		"success":  provider.ChecksSuccess,
		"failed":   provider.ChecksFailure,
		"canceled": provider.ChecksFailure,
		"running":  provider.ChecksPending,
		"manual":   provider.ChecksPending,
		"":         provider.ChecksNone,
	}
	for status, want := range cases {
		t.Run(status, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/pipelines") {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if status == "" {
					_ = json.NewEncoder(w).Encode([]any{})
					return
				}
				_ = json.NewEncoder(w).Encode([]map[string]any{{"status": status}})
			}))
			defer srv.Close()
			p := New(srv.URL+"/api/v4", "o/r", "token", srv.Client())
			got, err := p.Checks(context.Background(), &provider.PR{Number: 5, HeadSHA: "sha1"})
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("got %s, want %s", got, want)
			}
		})
	}
}

func TestCreateConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Another open merge request already exists for this source branch"}`))
	}))
	defer srv.Close()
	p := New(srv.URL, "o/r", "token", srv.Client())
	_, err := p.CreatePR(context.Background(), provider.CreateRequest{Head: "h", Base: "b", Title: "t"})
	if !errors.Is(err, provider.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestMergeStrategyBody(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	p := New(srv.URL, "o/r", "token", srv.Client())
	if err := p.Merge(context.Background(), 5, "squash"); err != nil {
		t.Fatal(err)
	}
	if squash, _ := got["squash"].(bool); !squash {
		t.Fatalf("squash flag missing: %+v", got)
	}
	if err := p.Merge(context.Background(), 5, "rebase"); err == nil {
		t.Fatal("gitlab accepted rebase")
	}
}
