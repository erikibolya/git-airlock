package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getlantern/systray"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/erikibolya/git-airlock/internal/config"
	"github.com/erikibolya/git-airlock/internal/gitutil"
	"github.com/erikibolya/git-airlock/internal/install"
	"github.com/erikibolya/git-airlock/internal/reposync"
	"github.com/erikibolya/git-airlock/internal/ui"
)

//go:embed tray.ico tray.png
var trayFS embed.FS

type dashBox struct {
	dash    *ui.Dashboard
	handler http.Handler
	cfgs    []*config.Config
	paths   []string
}

func main() {
	if hasFlag("--notify-test") {
		notify("Git Airlock", "Native notifications are working.")
		return
	}
	argPaths := argValues("--config")
	paths := argPaths
	if len(paths) == 0 {
		paths = loadSavedConfigs()
	}
	if len(paths) == 0 {
		if p, err := config.Resolve(""); err == nil {
			paths = []string{p}
		}
	}

	app := &App{startHidden: hasFlag("--tray") || hasFlag("--minimized"), paths: paths}
	app.pendingNav.Store("")
	app.lastRepo.Store("")
	if hasFlag("--repos") || hasFlag("--setup") {
		app.setupMode.Store(true)
	}
	if box, err := buildBox(paths); err == nil {
		app.current.Store(box)
		_ = saveConfigs(paths)
	} else {
		app.loadErrs = []string{err.Error()}
		app.setupMode.Store(true)
	}

	err := wails.Run(&options.App{
		Title:            "Git Airlock",
		Width:            1150,
		Height:           760,
		MinWidth:         900,
		MinHeight:        600,
		StartHidden:      app.startHidden,
		AssetServer:      &assetserver.Options{Handler: app},
		BackgroundColour: &options.RGBA{R: 246, G: 248, B: 251, A: 1},
		OnStartup:        app.startup,
		OnBeforeClose:    app.beforeClose,
		OnShutdown:       app.shutdown,
		Menu:             app.menu(),
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "git-airlock-desktop",
			OnSecondInstanceLaunch: func(options.SecondInstanceData) {
				app.showWindow()
			},
		},
		Bind: []interface{}{app},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "git-airlock desktop:", err)
		os.Exit(1)
	}
}

// App is the Wails application: it serves the multi-repository dashboard and
// adds a tray icon, a repository picker and a native sync action.
type App struct {
	startHidden bool

	mu       sync.Mutex
	paths    []string
	loadErrs []string

	current    atomic.Pointer[dashBox]
	setupMode  atomic.Bool
	pendingNav atomic.Value
	lastRepo   atomic.Value

	ctx        context.Context
	quitting   atomic.Bool
	tray       atomic.Bool
	syncMu     sync.Mutex
	syncItem   *systray.MenuItem
	statusItem *systray.MenuItem
}

func buildBox(paths []string) (*dashBox, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("add at least one .airlock.toml")
	}
	var cfgs []*config.Config
	for _, p := range paths {
		cfg, err := config.Load(p)
		if err != nil {
			return nil, err
		}
		cfgs = append(cfgs, cfg)
	}
	dash, err := ui.NewDashboard(cfgs, "")
	if err != nil {
		return nil, err
	}
	return &dashBox{dash: dash, handler: dash.Handler(), cfgs: cfgs, paths: paths}, nil
}

// ServeHTTP is the Wails asset handler. The repository picker takes over while
// setupMode is set or until at least one config loads.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p := r.URL.Path; strings.HasPrefix(p, "/repo/") {
		rest := strings.TrimPrefix(p, "/repo/")
		if i := strings.IndexByte(rest, '/'); i > 0 {
			a.lastRepo.Store("/repo/" + rest[:i] + "/")
		}
	}
	// Menu navigation reloads the current page; redirect the document request
	// to the requested page before serving anything else.
	if target, _ := a.pendingNav.Load().(string); target != "" && strings.Contains(r.Header.Get("Accept"), "text/html") {
		a.pendingNav.Store("")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	// The dashboard links to /repositories; serve the native picker there.
	if r.URL.Path == "/repositories" || r.URL.Path == "/setup" {
		a.setupMode.Store(true)
		a.serveSetup(w, r)
		return
	}
	if !a.setupMode.Load() {
		if box := a.current.Load(); box != nil {
			box.handler.ServeHTTP(w, r)
			return
		}
	}
	a.serveSetup(w, r)
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if a.current.Load() == nil {
		// An autostarted app must not hide a broken configuration in the tray.
		wailsruntime.WindowShow(ctx)
	}
	go a.startTray()
	go a.watchResults()
}

// navigate reloads the webview and redirects the document request to path.
func (a *App) navigate(target string) {
	a.pendingNav.Store(target)
	a.setupMode.Store(target == "/repositories")
	if a.ctx != nil {
		wailsruntime.WindowReload(a.ctx)
	}
	a.showWindow()
}

func (a *App) repoBase() string {
	if base, _ := a.lastRepo.Load().(string); base != "" {
		return base
	}
	if box := a.current.Load(); box != nil {
		if repos := box.dash.Repos(); len(repos) > 0 {
			return "/repo/" + repos[0].Slug + "/"
		}
	}
	return "/"
}

func (a *App) navigateRepo(page string) {
	base := a.repoBase()
	if base == "/" {
		a.navigate("/")
		return
	}
	a.navigate(base + page)
}

func (a *App) quit() {
	a.quitting.Store(true)
	a.tray.Store(false)
	systray.Quit()
	if a.ctx != nil {
		wailsruntime.Quit(a.ctx)
	}
}

func (a *App) about() {
	if a.ctx == nil {
		return
	}
	var repos []string
	if box := a.current.Load(); box != nil {
		for _, cfg := range box.cfgs {
			repos = append(repos, cfg.Repo)
		}
	}
	msg := "Git Airlock\n\nhttps://github.com/erikibolya/git-airlock"
	if len(repos) > 0 {
		msg = "Git Airlock\n\nWatching: " + strings.Join(repos, ", ") + "\n\nhttps://github.com/erikibolya/git-airlock"
	}
	_, _ = wailsruntime.MessageDialog(a.ctx, wailsruntime.MessageDialogOptions{
		Type:    wailsruntime.InfoDialog,
		Title:   "About Git Airlock",
		Message: msg,
	})
}

// menu builds the native application menu; the tray remains for background use.
func (a *App) menu() *menu.Menu {
	m := menu.NewMenu()
	file := m.AddSubmenu("File")
	file.AddText("Dashboard", nil, func(*menu.CallbackData) { a.navigate("/") })
	file.AddText("Repositories…", nil, func(*menu.CallbackData) { a.navigate("/repositories") })
	file.AddSeparator()
	file.AddText("Sync now", nil, func(*menu.CallbackData) { go a.syncNow() })
	file.AddSeparator()
	file.AddText("Quit", keys.CmdOrCtrl("q"), func(*menu.CallbackData) { a.quit() })

	view := m.AddSubmenu("View")
	view.AddText("Manual", nil, func(*menu.CallbackData) { a.navigateRepo("manual") })
	view.AddText("Logs", nil, func(*menu.CallbackData) { a.navigateRepo("logs") })

	help := m.AddSubmenu("Help")
	help.AddText("Git Airlock on GitHub", nil, func(*menu.CallbackData) {
		if a.ctx != nil {
			wailsruntime.BrowserOpenURL(a.ctx, "https://github.com/erikibolya/git-airlock")
		}
	})
	help.AddText("About", nil, func(*menu.CallbackData) { a.about() })
	return m
}

func (a *App) showWindow() {
	if a.ctx == nil {
		return
	}
	wailsruntime.WindowShow(a.ctx)
	wailsruntime.WindowUnminimise(a.ctx)
	// Windows does not always give focus to a window shown by another process;
	// a short always-on-top pulse and a native raise bring it to the front.
	wailsruntime.WindowSetAlwaysOnTop(a.ctx, true)
	go func() {
		time.Sleep(120 * time.Millisecond)
		bringToFront()
		time.Sleep(180 * time.Millisecond)
		wailsruntime.WindowSetAlwaysOnTop(a.ctx, false)
	}()
}

func (a *App) beforeClose(context.Context) bool {
	if a.quitting.Load() || !a.tray.Load() {
		return false
	}
	wailsruntime.WindowHide(a.ctx)
	return true
}

func (a *App) shutdown(context.Context) {
	if a.tray.CompareAndSwap(true, false) {
		systray.Quit()
	}
}

// ---------- repository picker ----------

// PickResult is returned to the setup page.
type PickResult struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// AddConfig opens a native file dialog, validates the config and appends it.
func (a *App) AddConfig() PickResult {
	dir := ""
	a.mu.Lock()
	if len(a.paths) > 0 {
		dir = filepath.Dir(a.paths[len(a.paths)-1])
	}
	a.mu.Unlock()

	path, err := wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title:            "Select .airlock.toml",
		DefaultDirectory: dir,
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "Airlock config (*.toml)", Pattern: "*.toml"},
		},
	})
	if err != nil {
		return PickResult{Error: err.Error()}
	}
	if path == "" {
		return PickResult{}
	}
	if _, err := config.Load(path); err != nil {
		return PickResult{Error: err.Error()}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range a.paths {
		if p == path {
			return PickResult{Path: path}
		}
	}
	a.paths = append(a.paths, path)
	a.loadErrs = nil
	_ = saveConfigs(a.paths)
	return PickResult{Path: path}
}

// RemoveConfig drops a config from the list.
func (a *App) RemoveConfig(path string) PickResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.paths[:0]
	for _, p := range a.paths {
		if p != path {
			out = append(out, p)
		}
	}
	a.paths = out
	a.loadErrs = nil
	_ = saveConfigs(a.paths)
	return PickResult{}
}

// Start validates the current list, updates the worker task and opens the
// dashboard without restarting the process.
func (a *App) Start() PickResult {
	a.mu.Lock()
	paths := append([]string(nil), a.paths...)
	a.mu.Unlock()
	if len(paths) == 0 {
		return PickResult{Error: "Add at least one config first."}
	}
	box, err := buildBox(paths)
	if err != nil {
		a.mu.Lock()
		a.loadErrs = []string{err.Error()}
		a.mu.Unlock()
		return PickResult{Error: err.Error()}
	}
	if err := updateWorkerTask(paths); err != nil {
		return PickResult{Error: "worker task: " + err.Error()}
	}
	a.current.Store(box)
	_ = saveConfigs(paths)
	a.setupMode.Store(false)
	if a.syncItem != nil {
		a.syncItem.Enable()
	}
	return PickResult{}
}

// Cancel returns to the dashboard when one is already configured.
func (a *App) Cancel() {
	if a.current.Load() != nil {
		a.setupMode.Store(false)
	}
}

// updateWorkerTask points the scheduled worker at the same repositories. It is
// skipped for dev builds that do not ship airlock.exe next to the desktop exe.
func updateWorkerTask(paths []string) error {
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	name := "airlock"
	if goruntime.GOOS == "windows" {
		name = "airlock.exe"
	}
	exe := filepath.Join(filepath.Dir(self), name)
	if _, err := os.Stat(exe); err != nil {
		return nil
	}
	return install.Install(install.Options{
		Name:        "Git Airlock",
		ConfigPaths: paths,
		ExePath:     exe,
		Interval:    time.Minute,
	})
}

func (a *App) serveSetup(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	paths := append([]string(nil), a.paths...)
	loadErrs := append([]string(nil), a.loadErrs...)
	a.mu.Unlock()

	msg := "Add the repositories this dashboard should watch. Each one needs its own .airlock.toml."
	if len(loadErrs) > 0 {
		msg = strings.Join(loadErrs, "\n")
	}
	var list strings.Builder
	for _, p := range paths {
		esc := html.EscapeString(p)
		fmt.Fprintf(&list, `<li><code>%s</code><button class="remove" data-path="%s">remove</button></li>`, esc, esc)
	}
	listHTML := `<p class="empty">No configs yet.</p>`
	if list.Len() > 0 {
		listHTML = `<ul class="repos">` + list.String() + `</ul>`
	}
	cancelHTML := ""
	if a.current.Load() != nil {
		cancelHTML = `<button id="cancel">Cancel</button>`
	}
	page := strings.NewReplacer(
		"{{MSG}}", html.EscapeString(msg),
		"{{LIST}}", listHTML,
		"{{CANCEL}}", cancelHTML,
	).Replace(setupHTML)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(page))
}

const setupHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Git Airlock — repositories</title>
<style>
  :root { color-scheme: light; }
  body { margin:0; font:15px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; background:#f6f8fb; color:#0f172a; }
  main { max-width:760px; margin:0 auto; padding:48px 24px; }
  h1 { font-size:26px; margin:0 0 4px; }
  .sub { color:#475569; margin:0 0 28px; }
  .card { background:#fff; border:1px solid #e2e8f0; border-radius:14px; padding:24px; box-shadow:0 1px 2px rgba(15,23,42,.05); }
  label { display:block; font-weight:600; font-size:13px; margin-bottom:6px; }
  button { padding:10px 16px; border-radius:8px; border:1px solid #cbd5e1; background:#fff; font:600 13px system-ui; cursor:pointer; }
  button:hover { background:#f1f5f9; }
  button.primary { background:#2563eb; border-color:#2563eb; color:#fff; }
  button.primary:hover { background:#1d4ed8; }
  .actions { display:flex; justify-content:flex-end; gap:8px; margin-top:16px; }
  .status { margin-top:14px; font-size:13px; min-height:20px; }
  .status.err { color:#b91c1c; }
  .status.ok { color:#047857; }
  .hint { margin-top:22px; font-size:13px; color:#64748b; }
  code { background:#f1f5f9; padding:1px 5px; border-radius:4px; }
  .msg { background:#fff7ed; border:1px solid #fed7aa; color:#9a3412; border-radius:10px; padding:12px 14px; font-size:13px; margin-bottom:20px; white-space:pre-wrap; word-break:break-word; }
  ul.repos { list-style:none; margin:0; padding:0; }
  ul.repos li { display:flex; gap:12px; align-items:center; justify-content:space-between; padding:9px 0; border-bottom:1px solid #e2e8f0; }
  ul.repos li:last-child { border-bottom:0; }
  ul.repos code { font-size:12px; word-break:break-all; }
  button.remove { border:0; background:none; color:#b91c1c; padding:4px 6px; font-size:12px; }
  button.remove:hover { background:#fef2f2; }
  .empty { color:#64748b; font-size:13px; margin:0; }
</style>
</head>
<body>
<main>
  <h1>Git Airlock</h1>
  <p class="sub">Synchronises mirrors across the air gap and merges pull requests.</p>
  <div class="msg">{{MSG}}</div>
  <div class="card">
    <label>Repositories</label>
    {{LIST}}
    <div class="actions" style="justify-content:flex-start;margin-top:14px">
      <button id="add">Add config…</button>
    </div>
    <div class="actions">
      {{CANCEL}}
      <button id="start" class="primary">Open dashboard</button>
    </div>
    <div id="status" class="status"></div>
    <p class="hint">The scheduled worker is pointed at the same list, so these repositories are
    watched every minute. Create a config with <code>airlock init</code>.</p>
  </div>
</main>
<script>
(function () {
  var status = document.getElementById('status');
  function say(text, cls) { status.textContent = text || ''; status.className = 'status' + (cls ? ' ' + cls : ''); }
  function bind() { return window.go && window.go.main && window.go.main.App; }
  document.getElementById('add').addEventListener('click', function () {
    var app = bind();
    if (!app) { say('Native bindings are still loading, try again.', 'err'); return; }
    app.AddConfig().then(function (r) {
      if (r && r.error) { say(r.error, 'err'); return; }
      if (r && r.path) { window.location.reload(); }
    }).catch(function (e) { say(String(e), 'err'); });
  });
  var buttons = document.querySelectorAll('button.remove');
  for (var i = 0; i < buttons.length; i++) {
    buttons[i].addEventListener('click', function () {
      var app = bind();
      if (!app) { say('Native bindings are still loading, try again.', 'err'); return; }
      app.RemoveConfig(this.getAttribute('data-path')).then(function () { window.location.reload(); })
        .catch(function (e) { say(String(e), 'err'); });
    });
  }
  document.getElementById('start').addEventListener('click', function () {
    var app = bind();
    if (!app) { say('Native bindings are still loading, try again.', 'err'); return; }
    say('Checking…');
    app.Start().then(function (r) {
      if (r && r.error) { say(r.error, 'err'); return; }
      say('Starting…', 'ok');
      window.location.replace('/');
    }).catch(function (e) { say(String(e), 'err'); });
  });
  var cancel = document.getElementById('cancel');
  if (cancel) {
    cancel.addEventListener('click', function () {
      var app = bind();
      if (!app) { return; }
      app.Cancel();
      window.location.replace('/');
    });
  }
})();
</script>
</body>
</html>
`

// ---------- tray ----------

func (a *App) startTray() {
	iconFile := "tray.png"
	if goruntime.GOOS == "windows" {
		iconFile = "tray.ico"
	}
	icon, err := trayFS.ReadFile(iconFile)
	if err != nil {
		return
	}
	systray.Run(func() { a.onReady(icon) }, func() {})
}

func (a *App) onReady(icon []byte) {
	systray.SetIcon(icon)
	systray.SetTitle("Git Airlock")
	systray.SetTooltip("Git Airlock")
	mOpen := systray.AddMenuItem("Open dashboard", "Show the Git Airlock window")
	mRepos := systray.AddMenuItem("Repositories…", "Choose which repositories the dashboard watches")
	mStatus := systray.AddMenuItem("No activity yet", "Latest worker result; click to open the dashboard")
	mSync := systray.AddMenuItem("Sync now", "Sync mirrors with the private remote")
	if a.current.Load() != nil {
		mSync.Enable()
	} else {
		mSync.Disable()
	}
	a.syncItem = mSync
	a.statusItem = mStatus
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Quit Git Airlock")
	a.tray.Store(true)

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				if a.setupMode.CompareAndSwap(true, false) && a.ctx != nil {
					wailsruntime.WindowReload(a.ctx)
				}
				a.showWindow()
			case <-mRepos.ClickedCh:
				a.setupMode.Store(true)
				if a.ctx != nil {
					wailsruntime.WindowReload(a.ctx)
				}
				a.showWindow()
			case <-mStatus.ClickedCh:
				a.showWindow()
			case <-mSync.ClickedCh:
				go a.syncNow()
			case <-mQuit.ClickedCh:
				a.quit()
				return
			}
		}
	}()
}

func (a *App) syncNow() {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	box := a.current.Load()
	if box == nil {
		return
	}
	systray.SetTooltip("Git Airlock: syncing…")
	var parts []string
	for _, cfg := range box.cfgs {
		report, err := reposync.Sync(context.Background(), cfg)
		parts = append(parts, cfg.Repo+": "+syncMessage(report, err))
	}
	msg := join(parts)
	systray.SetTooltip(truncate("Git Airlock: "+msg, 120))
}

func syncMessage(r gitutil.SyncReport, err error) string {
	var parts []string
	if len(r.Pushed) > 0 {
		parts = append(parts, "pushed "+join(r.Pushed))
	}
	if len(r.FastForwarded) > 0 {
		parts = append(parts, "fast-forwarded "+join(r.FastForwarded))
	}
	if len(r.Diverged) > 0 {
		parts = append(parts, "DIVERGED "+join(r.Diverged))
	}
	if err != nil {
		parts = append(parts, "error: "+err.Error())
	} else if len(parts) == 0 {
		parts = append(parts, "up to date")
	}
	return join(parts)
}

func join(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---------- saved state / args ----------

type desktopState struct {
	// Config is the pre-0.9 single-repository field.
	Config  string   `json:"config,omitempty"`
	Configs []string `json:"configs,omitempty"`
}

func statePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "git-airlock", "desktop.json")
}

func loadSavedConfigs() []string {
	path := statePath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var st desktopState
	if json.Unmarshal(data, &st) != nil {
		return nil
	}
	paths := st.Configs
	if len(paths) == 0 && st.Config != "" {
		paths = []string{st.Config}
	}
	var out []string
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

func saveConfigs(paths []string) error {
	path := statePath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(desktopState{Configs: paths}, "", "  ")
	return os.WriteFile(path, data, 0o644)
}

func argValues(name string) []string {
	var out []string
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == name && i+1 < len(os.Args) {
			out = append(out, os.Args[i+1])
			i++
			continue
		}
		if strings.HasPrefix(os.Args[i], name+"=") {
			out = append(out, strings.TrimPrefix(os.Args[i], name+"="))
		}
	}
	return out
}

func hasFlag(name string) bool {
	for _, a := range os.Args[1:] {
		if a == name {
			return true
		}
	}
	return false
}
