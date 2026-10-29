package ui

import (
	"net/http"
	"net/http/httptest"
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

func TestDashboard(t *testing.T) {
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
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(rr.Body.String(), "Recent activity") {
		t.Fatal("overview does not show activity")
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/manual", nil))
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/" {
		t.Fatalf("GET /manual = %d, location %q", rr.Code, rr.Header().Get("Location"))
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

func TestDashboardListsRepos(t *testing.T) {
	cfgA := testConfig(t)
	cfgA.Repo = "owner/one"
	cfgB := testConfig(t)
	cfgB.Repo = "owner/two"
	cfgB.Base = "master"

	qA := queue.New(cfgA.Queue)
	id, err := qA.Enqueue(&queue.Request{Action: queue.ActionStatus, Number: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := qA.WriteResult(&queue.Result{ID: id, Action: queue.ActionStatus, OK: true, Message: "manual-result-visible", PR: 3}); err != nil {
		t.Fatal(err)
	}

	d, err := NewDashboard([]*config.Config{cfgA, cfgB}, "")
	if err != nil {
		t.Fatal(err)
	}
	h := d.Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"owner/one", "owner/two", "repositories"} {
		if !strings.Contains(body, want) {
			t.Fatalf("repo list missing %q", want)
		}
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/repo/owner-one/", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Recent activity") {
		t.Fatalf("detail page = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "all repositories") {
		t.Fatal("detail page has no link back to the list")
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/repo/owner-one/", nil))
	if !strings.Contains(rr.Body.String(), "manual-result-visible") {
		t.Fatalf("detail page does not render results: %s", rr.Body.String())
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/repo/owner-one/manual", nil))
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/repo/owner-one/" {
		t.Fatalf("manual redirect = %d, location %q", rr.Code, rr.Header().Get("Location"))
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/repositories", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "airlock ui --config") {
		t.Fatalf("repositories page = %d", rr.Code)
	}
}
