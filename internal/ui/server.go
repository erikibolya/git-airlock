// Package ui is the optional local web dashboard for Git Airlock.
package ui

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/erikibolya/git-airlock/internal/config"
	"github.com/erikibolya/git-airlock/internal/queue"
)

//go:embed templates/*.html
var templateFS embed.FS

var pages = map[string]*template.Template{}

func init() {
	for _, name := range []string{"repos", "repositories", "overview", "result", "logs"} {
		pages[name] = template.Must(template.ParseFS(templateFS, "templates/layout.html", "templates/"+name+".html"))
	}
}

// Server is the dashboard. It only reads the queue and enqueues requests; the
// worker remains the only component that talks to the git host.
type Server struct {
	cfg      *config.Config
	store    queue.Store
	token    string
	basePath string
	authMode string

	cacheMu  sync.Mutex
	cacheAt  time.Time
	cacheVal repoSummary
	cacheErr error
}

// New builds a dashboard. An empty token disables authentication (meant for
// loopback binds only).
func New(cfg *config.Config, accessToken string) (*Server, error) {
	store, err := queue.Open(cfg.Queue, cfg.Mirror, cfg.QueueRemote)
	if err != nil {
		return nil, err
	}
	if err := store.Ensure(); err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, store: store, token: accessToken}, nil
}

// NewAccessToken returns a random URL-safe token.
func NewAccessToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "fallback-token"
	}
	return hex.EncodeToString(b[:])
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleOverview)
	mux.HandleFunc("/manual", func(w http.ResponseWriter, r *http.Request) {
		target := s.basePath
		if target == "" {
			target = "/"
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	})
	mux.HandleFunc("/result/", s.handleResult)
	mux.HandleFunc("/logs", s.handleLogs)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	return headers(auth(s.token, mux))
}

// auth requires the dashboard token when one is configured. An empty token
// leaves the handler open (loopback and the embedded desktop window).
func auth(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if t := r.URL.Query().Get("token"); t != "" && tokenEqual(t, token) {
			http.SetCookie(w, &http.Cookie{
				Name:     "airlock_token",
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			q := r.URL.Query()
			q.Del("token")
			r.URL.RawQuery = q.Encode()
			http.Redirect(w, r, r.URL.String(), http.StatusSeeOther)
			return
		}
		c, err := r.Cookie("airlock_token")
		if err != nil || !tokenEqual(c.Value, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

type pageData struct {
	Title       string
	Config      *config.Config
	Flash       string
	Error       string
	Result      *queue.Result
	ResultJSON  string
	Logs        string
	QueueMode   string
	AuthMode    string
	Now         time.Time
	BasePath    string
	AutoRefresh bool
	Desktop     bool

	// Overview page.
	PendingItems []pendingItem
	Activity     []activityItem
	Active       bool
	Waiting      bool
	Status       string
	StatusTitle  string
	StatusSub    string
	LastActivity string
	LastCheck    string

	// Repository list.
	Repos         []repoView
	RepoCount     int
	RepoCountText string
	ActiveCount   int
	WaitingCount  int
}

type repoView struct {
	Link         string
	Repo         string
	Provider     string
	Base         string
	AutoPR       bool
	Merge        string
	Pending      int
	Status       string
	StatusTitle  string
	LastActivity string
	LastCheck    string
	Error        string
}

type repoSummary struct {
	PendingItems []pendingItem
	Activity     []activityItem
	Active       bool
	Waiting      bool
	Status       string
	StatusTitle  string
	StatusSub    string
	LastActivity string
	LastCheck    string
}

type pendingItem struct {
	Request queue.Request
	Label   string
	Detail  string
	Checks  string
	PR      int
	Since   string
	Pending bool
}

type activityItem struct {
	At      string
	Label   string
	Message string
	State   string
	Checks  string
	URL     string
}

func (s *Server) baseData(r *http.Request, title string) pageData {
	mode := "directory"
	if s.cfg.Queue == "git" {
		mode = "git refs"
	}
	authMode := s.authMode
	if authMode == "" {
		authMode = "localhost only"
		if s.token != "" {
			authMode = "token protected"
		}
	}
	return pageData{Title: title, Config: s.cfg, QueueMode: mode, AuthMode: authMode, BasePath: s.basePath, Now: time.Now(), Desktop: isDesktopRequest(r)}
}

// isDesktopRequest reports whether the page is being served inside the Wails
// window. The desktop app sets this header; the native menu already provides
// navigation there, so the in-page nav is hidden.
func isDesktopRequest(r *http.Request) bool {
	return r.Header.Get("X-Airlock-Desktop") == "1"
}

func (s *Server) render(w http.ResponseWriter, name string, data pageData) {
	var buf bytes.Buffer
	if err := pages[name].ExecuteTemplate(&buf, "layout.html", data); err != nil {
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

// handleOverview is the repository detail page: what is watched, what the
// worker is doing, and the latest results.
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	sum, err := s.summary()
	data := s.baseData(r, "Overview")
	data.Flash = r.URL.Query().Get("flash")
	if err != nil {
		data.Error = err.Error()
		s.render(w, "overview", data)
		return
	}
	data.AutoRefresh = sum.Active || sum.Waiting
	data.PendingItems = sum.PendingItems
	data.Activity = sum.Activity
	data.Active = sum.Active
	data.Waiting = sum.Waiting
	data.Status = sum.Status
	data.StatusTitle = sum.StatusTitle
	data.StatusSub = sum.StatusSub
	data.LastActivity = sum.LastActivity
	data.LastCheck = sum.LastCheck
	s.render(w, "overview", data)
}

// summary returns a short-lived cached snapshot. Navigating between pages
// should not re-run git for the same queue state; the cache is invalidated by
// the request forms below.
func (s *Server) summary() (repoSummary, error) {
	s.cacheMu.Lock()
	if !s.cacheAt.IsZero() && time.Since(s.cacheAt) < 3*time.Second {
		val, err := s.cacheVal, s.cacheErr
		s.cacheMu.Unlock()
		return val, err
	}
	s.cacheMu.Unlock()

	val, err := s.readSummary()
	s.cacheMu.Lock()
	s.cacheVal, s.cacheErr, s.cacheAt = val, err, time.Now()
	s.cacheMu.Unlock()
	return val, err
}

func (s *Server) invalidateSummary() {
	s.cacheMu.Lock()
	s.cacheAt = time.Time{}
	s.cacheMu.Unlock()
}

// readSummary reads the queue once and derives the state shown on both the
// repository list and the detail page.
func (s *Server) readSummary() (repoSummary, error) {
	var sum repoSummary
	pending, err := s.store.Pending()
	if err != nil {
		return sum, err
	}
	results, _ := s.store.ListResults(25)
	byID := make(map[string]queue.Result, len(results))
	for _, res := range results {
		byID[res.ID] = res
	}
	sum.Active = len(pending) > 0

	for _, req := range pending {
		item := pendingItem{
			Request: req,
			Label:   requestLabel(req),
			Detail:  "queued; the worker picks it up within a minute",
			Since:   ageText(idTime(req.ID)),
		}
		if res, ok := byID[req.ID]; ok {
			item.Detail = res.Message
			item.Checks = res.Checks
			item.PR = res.PR
			item.Pending = res.Pending
		} else if res, err := s.store.ReadResult(req.ID); err == nil && res != nil {
			// The result is older than the 25 shown in activity.
			item.Detail = res.Message
			item.Checks = res.Checks
			item.PR = res.PR
			item.Pending = res.Pending
		}
		if item.Pending || item.Checks == "pending" {
			sum.Waiting = true
		}
		sum.PendingItems = append(sum.PendingItems, item)
	}

	for _, res := range results {
		sum.Activity = append(sum.Activity, activityItem{
			At:      ageText(idTime(res.ID)),
			Label:   resultLabel(res),
			Message: res.Message,
			State:   resultState(res),
			Checks:  res.Checks,
			URL:     res.URL,
		})
		if sum.LastCheck == "" && res.Checks != "" && res.Checks != "none" {
			if res.PR > 0 {
				sum.LastCheck = fmt.Sprintf("PR #%d, checks %s, %s", res.PR, res.Checks, ageText(idTime(res.ID)))
			} else {
				sum.LastCheck = fmt.Sprintf("checks %s, %s", res.Checks, ageText(idTime(res.ID)))
			}
		}
	}
	if len(results) > 0 {
		sum.LastActivity = ageText(idTime(results[0].ID))
	}

	switch {
	case sum.Waiting:
		sum.Status = "waiting"
		sum.StatusTitle = "Waiting for CI"
		sum.StatusSub = "The worker checks every minute and merges when the checks are green."
	case sum.Active:
		sum.Status = "queued"
		sum.StatusTitle = "Working"
		sum.StatusSub = "A request is queued; the worker will execute it within a minute."
	default:
		sum.Status = "idle"
		sum.StatusTitle = "Idle"
		if sum.LastActivity != "" {
			sum.StatusSub = "Everything is synced. Last activity " + sum.LastActivity + "."
		} else {
			sum.StatusSub = "Everything is synced. Nothing to do."
		}
	}
	return sum, nil
}

func requestLabel(req queue.Request) string {
	switch req.Action {
	case queue.ActionOpen:
		base := req.Base
		if base == "" {
			base = "base"
		}
		return fmt.Sprintf("Open PR %s -> %s", req.Head, base)
	case queue.ActionStatus:
		return fmt.Sprintf("Check PR #%d", req.Number)
	case queue.ActionMerge:
		return fmt.Sprintf("Merge PR #%d", req.Number)
	case queue.ActionSync:
		return "Sync mirror"
	default:
		return req.Action
	}
}

func resultLabel(res queue.Result) string {
	switch res.Action {
	case queue.ActionOpen:
		return "Open PR"
	case queue.ActionStatus:
		return fmt.Sprintf("Check PR #%d", res.PR)
	case queue.ActionMerge:
		return fmt.Sprintf("Merge PR #%d", res.PR)
	case queue.ActionSync:
		return "Sync mirror"
	default:
		return res.Action
	}
}

func resultState(res queue.Result) string {
	switch {
	case res.Pending:
		return "waiting"
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

func idTime(id string) time.Time {
	if len(id) < 15 {
		return time.Time{}
	}
	t, err := time.ParseInLocation("20060102-150405", id[:15], time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

func ageText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	default:
		return t.Format("2 Jan 15:04")
	}
}

func (s *Server) handleResult(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/result/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	res, err := s.store.ReadResult(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data := s.baseData(r, "Result")
	data.Result = res
	data.ResultJSON = pretty(res)
	s.render(w, "result", data)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := s.store.TailLog(250)
	data := s.baseData(r, "Logs")
	if err != nil {
		data.Error = err.Error()
	}
	data.Logs = logs
	s.render(w, "logs", data)
}

func pretty(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err.Error()
	}
	return string(data) + "\n"
}

func tokenEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
