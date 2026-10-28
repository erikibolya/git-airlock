package ui

import (
	"bytes"
	"fmt"
	"net/http"
	"time"

	"github.com/erikibolya/git-airlock/internal/config"
)

// Repo is one repository in the dashboard.
type Repo struct {
	Slug   string
	Config *config.Config
	Server *Server
}

// Dashboard shows a list of repositories and delegates per-repository pages to
// one Server each. It owns authentication for the whole tree.
type Dashboard struct {
	token string
	csrf  string
	repos []*Repo
}

// NewDashboard builds a dashboard for one or more configs.
func NewDashboard(cfgs []*config.Config, accessToken string) (*Dashboard, error) {
	if len(cfgs) == 0 {
		return nil, fmt.Errorf("at least one config is required")
	}
	d := &Dashboard{token: accessToken, csrf: NewAccessToken()}
	seen := map[string]int{}
	for _, cfg := range cfgs {
		slug := config.Slug(cfg.Repo)
		seen[slug]++
		if seen[slug] > 1 {
			slug = fmt.Sprintf("%s-%d", slug, seen[slug])
		}
		srv, err := New(cfg, "")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", cfg.Repo, err)
		}
		srv.basePath = "/repo/" + slug + "/"
		srv.authMode = authLabel(accessToken)
		d.repos = append(d.repos, &Repo{Slug: slug, Config: cfg, Server: srv})
	}
	return d, nil
}

func authLabel(token string) string {
	if token != "" {
		return "token protected"
	}
	return "localhost only"
}

// Repos exposes the configured repositories.
func (d *Dashboard) Repos() []*Repo { return d.repos }

// Handler returns the HTTP handler for the repository list and every
// per-repository dashboard.
func (d *Dashboard) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", d.handleIndex)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	for _, repo := range d.repos {
		prefix := "/repo/" + repo.Slug
		mux.Handle(prefix+"/", http.StripPrefix(prefix, repo.Server.Handler()))
	}
	return headers(auth(d.token, mux))
}

func (d *Dashboard) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data := pageData{
		Title:       "Repositories",
		CSRF:        d.csrf,
		Now:         time.Now(),
		AutoRefresh: true,
		AuthMode:    authLabel(d.token),
		RepoCount:   len(d.repos),
	}
	data.RepoCountText = reposText(data.RepoCount)
	for _, repo := range d.repos {
		view := repoView{
			Link:     "/repo/" + repo.Slug + "/",
			Repo:     repo.Config.Repo,
			Provider: repo.Config.Provider,
			Base:     repo.Config.Base,
			AutoPR:   repo.Config.AutoPR,
			Merge:    repo.Config.Merge,
		}
		sum, err := repo.Server.summary()
		if err != nil {
			view.Status = "failed"
			view.StatusTitle = "Error"
			view.Error = err.Error()
		} else {
			view.Pending = len(sum.PendingItems)
			view.Status = sum.Status
			view.StatusTitle = sum.StatusTitle
			view.LastActivity = sum.LastActivity
			view.LastCheck = sum.LastCheck
		}
		if view.Pending > 0 {
			data.ActiveCount++
		}
		if view.Status == "waiting" {
			data.WaitingCount++
		}
		data.Repos = append(data.Repos, view)
	}
	switch {
	case data.WaitingCount > 0:
		data.Status = "waiting"
		data.StatusTitle = "Waiting for CI"
		data.StatusSub = fmt.Sprintf("%d of %d repositories waiting for checks.", data.WaitingCount, data.RepoCount)
	case data.ActiveCount > 0:
		data.Status = "queued"
		data.StatusTitle = "Working"
		data.StatusSub = fmt.Sprintf("%d of %d repositories with a queued request.", data.ActiveCount, data.RepoCount)
	default:
		data.Status = "idle"
		data.StatusTitle = "All clear"
		data.StatusSub = data.RepoCountText + " watched. Everything is synced."
	}
	d.render(w, "repos", data)
}

func (d *Dashboard) render(w http.ResponseWriter, name string, data pageData) {
	var buf bytes.Buffer
	if err := pages[name].ExecuteTemplate(&buf, "layout.html", data); err != nil {
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func reposText(n int) string {
	if n == 1 {
		return "1 repository"
	}
	return fmt.Sprintf("%d repositories", n)
}
