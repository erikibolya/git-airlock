package ui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erikibolya/git-airlock/internal/config"
	"github.com/erikibolya/git-airlock/internal/queue"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		Repo: "owner/name", Provider: "github",
		Mirror: filepath.Join(t.TempDir(), "mirror.git"),
		Queue:  t.TempDir(), Base: "main", Merge: "when-green", Remote: "company",
	}
}

func TestDashboardAndEnqueue(t *testing.T) {
	cfg := testConfig(t)
	s, err := New(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "owner/name") {
		t.Fatal("dashboard does not show the repo")
	}
	if !strings.Contains(rr.Body.String(), "Recent activity") {
		t.Fatal("overview does not show activity")
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/manual", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Queue open PR") {
		t.Fatalf("GET /manual = %d", rr.Code)
	}

	form := url.Values{"csrf": {s.csrf}, "head": {"feature"}, "base": {"main"}, "merge": {"when-green"}}
	req := httptest.NewRequest(http.MethodPost, "/pr/open", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("POST /pr/open = %d (%s)", rr.Code, rr.Body.String())
	}
	pending, err := queue.New(cfg.Queue).Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Head != "feature" || pending[0].Merge != "when-green" {
		t.Fatalf("unexpected pending: %+v", pending)
	}
}

func TestResultAndLogs(t *testing.T) {
	cfg := testConfig(t)
	s, err := New(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	q := queue.New(cfg.Queue)
	id, err := q.Enqueue(&queue.Request{Action: queue.ActionStatus, Number: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.WriteResult(&queue.Result{ID: id, Action: queue.ActionStatus, OK: true, Message: "hello", PR: 3}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/result/"+id, nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "hello") {
		t.Fatalf("result page = %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/logs", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("logs page = %d", rr.Code)
	}
}

func TestAuth(t *testing.T) {
	cfg := testConfig(t)
	s, err := New(cfg, "secret")
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("without token = %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/?token=secret", nil))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("token bootstrap = %d", rr.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "airlock_token", Value: "secret"})
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("with cookie = %d", rr.Code)
	}
}

func TestCSRF(t *testing.T) {
	cfg := testConfig(t)
	s, err := New(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf": {"wrong"}, "head": {"feature"}, "base": {"main"}}
	req := httptest.NewRequest(http.MethodPost, "/pr/open", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("bad csrf = %d", rr.Code)
	}
}
