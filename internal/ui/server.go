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
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/erikibolya/git-airlock/internal/config"
	"github.com/erikibolya/git-airlock/internal/gitutil"
	"github.com/erikibolya/git-airlock/internal/proc"
	"github.com/erikibolya/git-airlock/internal/queue"
)

//go:embed templates/*.html
var templateFS embed.FS

var pages = map[string]*template.Template{}

func init() {
	for _, name := range []string{"index", "result", "logs"} {
		pages[name] = template.Must(template.ParseFS(templateFS, "templates/layout.html", "templates/"+name+".html"))
	}
}

// Server is the dashboard. It only reads the queue and enqueues requests; the
// worker remains the only component that talks to the git host.
type Server struct {
	cfg   *config.Config
	store queue.Store
	csrf  string
	token string
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
	return &Server{cfg: cfg, store: store, csrf: NewAccessToken(), token: accessToken}, nil
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
	mux.HandleFunc("/", s.auth(s.handleIndex))
	mux.HandleFunc("/pr/open", s.auth(s.handleOpen))
	mux.HandleFunc("/pr/status", s.auth(s.handleStatus))
	mux.HandleFunc("/pr/merge", s.auth(s.handleMerge))
	mux.HandleFunc("/sync", s.auth(s.handleSync))
	mux.HandleFunc("/result/", s.auth(s.handleResult))
	mux.HandleFunc("/logs", s.auth(s.handleLogs))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	return s.headers(mux)
}

func (s *Server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	if s.token == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if t := r.URL.Query().Get("token"); t != "" && tokenEqual(t, s.token) {
			http.SetCookie(w, &http.Cookie{
				Name:     "airlock_token",
				Value:    s.token,
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
		if err != nil || !tokenEqual(c.Value, s.token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

type pageData struct {
	Title      string
	Config     *config.Config
	CSRF       string
	Flash      string
	Error      string
	Pending    []queue.Request
	Results    []queue.Result
	Result     *queue.Result
	ResultJSON string
	Logs       string
	QueueMode  string
	AuthMode   string
	Now        time.Time
}

func (s *Server) baseData(title string) pageData {
	mode := "directory"
	if s.cfg.Queue == "git" {
		mode = "git refs"
	}
	auth := "localhost only"
	if s.token != "" {
		auth = "token protected"
	}
	return pageData{Title: title, Config: s.cfg, CSRF: s.csrf, QueueMode: mode, AuthMode: auth, Now: time.Now()}
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

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	pending, err := s.store.Pending()
	if err != nil {
		data := s.baseData("Dashboard")
		data.Error = err.Error()
		s.render(w, "index", data)
		return
	}
	results, _ := s.store.ListResults(25)
	data := s.baseData("Dashboard")
	data.Pending = pending
	data.Results = results
	data.Flash = r.URL.Query().Get("flash")
	s.render(w, "index", data)
}

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.formOK(w, r) {
		return
	}
	head := strings.TrimSpace(r.FormValue("head"))
	base := strings.TrimSpace(r.FormValue("base"))
	if base == "" {
		base = s.cfg.Base
	}
	if head == "" {
		s.flash(w, r, "head branch is required")
		return
	}
	if head == base {
		s.flash(w, r, "head and base must differ")
		return
	}
	merge := r.FormValue("merge")
	if merge != "never" && merge != "when-green" && merge != "now" {
		merge = s.cfg.Merge
	}
	headSHA := ""
	if out, err := proc.Command("git", "-C", s.cfg.Mirror, "rev-parse", "refs/heads/"+head).Output(); err == nil {
		headSHA = strings.TrimSpace(string(out))
	}
	req := &queue.Request{
		Action:  queue.ActionOpen,
		Head:    head,
		HeadSHA: headSHA,
		Base:    base,
		Title:   strings.TrimSpace(r.FormValue("title")),
		Body:    strings.TrimSpace(r.FormValue("body")),
		Merge:   merge,
		DryRun:  r.FormValue("dry_run") != "",
	}
	id, err := s.store.Enqueue(req)
	if err != nil {
		s.flash(w, r, "queue: "+err.Error())
		return
	}
	s.flash(w, r, fmt.Sprintf("queued %s (open %s -> %s)", id, head, base))
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.formOK(w, r) {
		return
	}
	number := formNumber(r, "number")
	if number <= 0 {
		s.flash(w, r, "PR number is required")
		return
	}
	id, err := s.store.Enqueue(&queue.Request{Action: queue.ActionStatus, Number: number})
	if err != nil {
		s.flash(w, r, "queue: "+err.Error())
		return
	}
	s.flash(w, r, fmt.Sprintf("queued %s (status #%d)", id, number))
}

func (s *Server) handleMerge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.formOK(w, r) {
		return
	}
	number := formNumber(r, "number")
	if number <= 0 {
		s.flash(w, r, "PR number is required")
		return
	}
	merge := "now"
	if r.FormValue("when_green") != "" {
		merge = "when-green"
	}
	id, err := s.store.Enqueue(&queue.Request{Action: queue.ActionMerge, Number: number, Merge: merge})
	if err != nil {
		s.flash(w, r, "queue: "+err.Error())
		return
	}
	s.flash(w, r, fmt.Sprintf("queued %s (merge #%d, %s)", id, number, merge))
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.formOK(w, r) {
		return
	}
	report, err := gitutil.Sync(r.Context(), s.cfg.Mirror, s.cfg.Remote)
	var parts []string
	if len(report.Pushed) > 0 {
		parts = append(parts, "pushed "+strings.Join(report.Pushed, ", "))
	}
	if len(report.FastForwarded) > 0 {
		parts = append(parts, "fast-forwarded "+strings.Join(report.FastForwarded, ", "))
	}
	if len(report.Diverged) > 0 {
		parts = append(parts, "DIVERGED "+strings.Join(report.Diverged, ", "))
	}
	if err != nil {
		parts = append(parts, "error: "+err.Error())
	} else if len(parts) == 0 {
		parts = append(parts, "already up to date")
	}
	s.flash(w, r, "sync: "+strings.Join(parts, "; "))
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
	data := s.baseData("Result")
	data.Result = res
	data.ResultJSON = pretty(res)
	s.render(w, "result", data)
}

func (s *Server) handleLogs(w http.ResponseWriter, _ *http.Request) {
	logs, err := s.store.TailLog(250)
	data := s.baseData("Logs")
	if err != nil {
		data.Error = err.Error()
	}
	data.Logs = logs
	s.render(w, "logs", data)
}

func (s *Server) formOK(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return false
	}
	if !tokenEqual(r.FormValue("csrf"), s.csrf) {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return false
	}
	return true
}

func (s *Server) flash(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/?flash="+url.QueryEscape(msg), http.StatusSeeOther)
}

func formNumber(r *http.Request, name string) int {
	n, err := strconv.Atoi(strings.TrimSpace(r.FormValue(name)))
	if err != nil || n <= 0 {
		return 0
	}
	return n
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
