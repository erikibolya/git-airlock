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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/systray"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/erikibolya/git-airlock/internal/config"
	"github.com/erikibolya/git-airlock/internal/configlist"
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
	if hasFlag("--check-updates") {
		if err := checkUpdatesCLI(); err != nil {
			fmt.Fprintln(os.Stderr, "update check:", err)
			os.Exit(1)
		}
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
		BackgroundColour: &options.RGBA{R: 238, G: 242, B: 247, A: 1},
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
	setupHits  atomic.Uint64
	pendingNav atomic.Value
	lastRepo   atomic.Value

	ctx        context.Context
	quitting   atomic.Bool
	tray       atomic.Bool
	tip        atomic.Value
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
	// Hide the in-page navigation in the desktop window; the native menu
	// already has Dashboard, Repositories and Logs.
	r.Header.Set("X-Airlock-Desktop", "1")

	// The picker is plain HTML served by us, not the generated Wails frontend,
	// so it cannot use window.go bindings. These endpoints back its buttons.
	if strings.HasPrefix(r.URL.Path, "/_airlock/") {
		a.serveAction(w, r)
		return
	}
	if p := r.URL.Path; strings.HasPrefix(p, "/repo/") {
		rest := strings.TrimPrefix(p, "/repo/")
		if i := strings.IndexByte(rest, '/'); i > 0 {
			a.lastRepo.Store("/repo/" + rest[:i] + "/")
		}
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
	a.startUpdateCheck()
}

// navigate moves the webview to a dashboard path without showing the window
// first; the native menu can only be used while the window is already visible.
func (a *App) navigate(target string) {
	a.navigateTo(target, false)
}

// navigateFromTray is used when the window may be hidden. Show it before the
// JS runs; WebView2 can drop ExecuteScript on a hidden window.
func (a *App) navigateFromTray(target string) {
	a.navigateTo(target, true)
}

func (a *App) navigateTo(target string, show bool) {
	setup := target == "/repositories"
	a.setupMode.Store(setup)
	if show {
		a.showWindow()
	}
	if a.ctx != nil {
		wailsruntime.WindowExecJS(a.ctx, "window.location.assign("+strconv.Quote(target)+")")
	}
	if setup {
		// If WebView2 drops the navigation (strict CSP pages have been known
		// to), a reload still serves the picker: ServeHTTP returns the setup
		// page for every path while setupMode is set. Only do that when the
		// navigation did not reach ServeHTTP.
		before := a.setupHits.Load()
		time.AfterFunc(500*time.Millisecond, func() {
			if a.ctx != nil && a.setupMode.Load() && a.setupHits.Load() == before {
				wailsruntime.WindowReload(a.ctx)
			}
		})
	}
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
	msg := "Git Airlock " + version + "\n\nhttps://github.com/erikibolya/git-airlock"
	if len(repos) > 0 {
		msg = "Git Airlock " + version + "\n\nWatching: " + strings.Join(repos, ", ") + "\n\nhttps://github.com/erikibolya/git-airlock"
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
	file.AddText("Logs", nil, func(*menu.CallbackData) { a.navigateRepo("logs") })
	file.AddSeparator()
	file.AddText("Sync now", nil, func(*menu.CallbackData) { go a.syncNow() })
	file.AddSeparator()
	file.AddText("Quit", keys.CmdOrCtrl("q"), func(*menu.CallbackData) { a.quit() })

	help := m.AddSubmenu("Help")
	help.AddText("Check for updates…", nil, func(*menu.CallbackData) { go a.checkUpdates(true) })
	help.AddText("Install latest dev build…", nil, func(*menu.CallbackData) { go a.checkDevUpdates() })
	help.AddText("Revert to last stable version…", nil, func(*menu.CallbackData) { go a.revertToStable() })
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
	a.reloadSaved()
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

// reloadSaved retries the saved repository list when the dashboard never loaded.
// Windows autostarts the app at logon, often before a mapped drive that holds the
// configs is mounted; without this the picker stayed empty until the app was
// restarted, because the list was only read once in main.
func (a *App) reloadSaved() {
	if a.current.Load() != nil {
		return
	}
	paths := loadSavedConfigs()
	if len(paths) == 0 {
		return
	}
	box, err := buildBox(paths)
	if err != nil {
		return
	}
	a.mu.Lock()
	a.paths = paths
	a.loadErrs = nil
	a.mu.Unlock()
	a.current.Store(box)
	wasSetup := a.setupMode.Swap(false)
	if a.syncItem != nil {
		a.syncItem.Enable()
	}
	if wasSetup {
		wailsruntime.WindowExecJS(a.ctx, "window.location.assign('/')")
	}
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

// Cancel discards the draft repository list and returns to the dashboard.
func (a *App) Cancel() {
	if box := a.current.Load(); box != nil {
		a.mu.Lock()
		a.paths = append([]string(nil), box.paths...)
		a.loadErrs = nil
		a.mu.Unlock()
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

// serveAction implements the repository picker's buttons. It deliberately uses
// HTTP instead of Wails bindings: the picker replaces the generated frontend,
// so window.go is not available there.
func (a *App) serveAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var res PickResult
	switch r.URL.Path {
	case "/_airlock/repos/add":
		res = a.AddConfig()
	case "/_airlock/repos/remove":
		res = a.RemoveConfig(r.URL.Query().Get("path"))
	case "/_airlock/start":
		res = a.Start()
	case "/_airlock/cancel":
		a.Cancel()
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (a *App) serveSetup(w http.ResponseWriter, _ *http.Request) {
	a.setupHits.Add(1)
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
		note := ""
		if _, err := os.Stat(p); err != nil {
			// Keep the entry (removing it here lost the repository) and say why it
			// cannot be loaded yet, e.g. a network drive that is not mounted.
			note = ` <span class="missing">not reachable</span>`
		}
		fmt.Fprintf(&list, `<li><span class="path"><code>%s</code>%s</span><button class="remove" data-path="%s">remove</button></li>`, esc, note, esc)
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
  :root{
    color-scheme:light;
    --font:-apple-system,BlinkMacSystemFont,"Segoe UI",system-ui,sans-serif;
    --mono:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
    --bg:#eef2f7;--panel:#fff;--panel-2:#f6f8fb;--panel-3:#e9eef5;
    --border:#d5dde8;--border-soft:#e4eaf2;
    --text:#1e293b;--muted:#64748b;
    --accent:#0369a1;--accent-soft:#bae6fd;
    --ok:#16a34a;--danger:#e11d48;
    --err-bg:#fee2e2;--err-fg:#991b1b;--err-bd:#fecaca;
    --radius:8px;
  }
  @media (prefers-color-scheme:dark){
    :root{
      color-scheme:dark;
      --bg:#0d1117;--panel:#151a21;--panel-2:#1b222b;--panel-3:#232c37;
      --border:#2a3441;--border-soft:#222a34;
      --text:#e6edf3;--muted:#8b98a8;
      --accent:#2f81f7;--accent-soft:#1f4fa0;
      --ok:#22c55e;--danger:#fb7185;
      --err-bg:#2a1115;--err-fg:#fda4af;--err-bd:#7f1d1d;
    }
  }
  *{box-sizing:border-box}
  body{margin:0;min-height:100vh;display:flex;flex-direction:column;font:13px/1.45 var(--font);background:var(--bg);color:var(--text);-webkit-font-smoothing:antialiased}
  header{display:flex;align-items:center;gap:9px;height:52px;padding:0 16px;background:var(--panel);border-bottom:1px solid var(--border)}
  .brand-mark{width:24px;height:24px;border-radius:6px;background:var(--panel-3);border:1px solid var(--border);color:var(--text);display:grid;place-items:center;font:700 9px/1 var(--font);letter-spacing:.03em;flex:none}
  .brand-copy{display:flex;flex-direction:column;line-height:1.2}
  .brand-copy b{font-size:13px;font-weight:600}
  .brand-copy span{font-size:11px;color:var(--muted)}
  main{width:100%;max-width:720px;margin:0 auto;padding:24px 20px;flex:1}
  h1{font-size:15px;font-weight:600;margin:0 0 3px;color:var(--text)}
  .sub{color:var(--muted);margin:0 0 18px;font-size:12px}
  .card{background:var(--panel);border:1px solid var(--border);border-radius:var(--radius);padding:16px}
  label{display:block;font-size:10px;font-weight:600;color:var(--muted);margin:0 0 8px;text-transform:uppercase;letter-spacing:.05em}
  button{display:inline-flex;align-items:center;justify-content:center;gap:6px;padding:6px 11px;border-radius:7px;border:1px solid var(--border);background:var(--panel-2);color:var(--text);font:inherit;font-size:12.5px;font-weight:500;cursor:pointer;margin:0}
  button:hover{background:var(--panel-3)}
  button:active{transform:translateY(1px)}
  button.primary{background:var(--ok);border-color:var(--ok);color:#fff;font-weight:600}
  button.primary:hover{background:#15803d;border-color:#15803d}
  .actions{display:flex;gap:8px;margin-top:14px}
  .actions.end{justify-content:flex-end;border-top:1px solid var(--border-soft);padding-top:14px;margin-top:16px}
  .status{margin-top:12px;font-size:12px;min-height:18px;color:var(--muted)}
  .status.err{color:var(--danger)}
  .status.ok{color:var(--ok)}
  .hint{margin:14px 0 0;font-size:11.5px;color:var(--muted)}
  code{background:var(--panel-2);border:1px solid var(--border-soft);border-radius:4px;padding:0 4px;font:11.5px/1.5 var(--mono);color:var(--text)}
  .msg{background:var(--panel-2);border:1px solid var(--border-soft);color:var(--muted);border-radius:var(--radius);padding:10px 12px;font-size:12px;margin-bottom:14px;white-space:pre-wrap;word-break:break-word}
  ul.repos{list-style:none;margin:0;padding:0}
  ul.repos li{display:flex;gap:12px;align-items:center;justify-content:space-between;padding:8px 0;border-bottom:1px solid var(--border-soft)}
  ul.repos li:last-child{border-bottom:0}
  ul.repos li>span.path{display:flex;align-items:center;gap:8px;min-width:0}
  ul.repos span.missing{color:var(--danger);font-size:11px;white-space:nowrap}
  ul.repos code{font-size:11.5px;word-break:break-all}
  button.remove{border-color:transparent;background:transparent;color:var(--danger);padding:4px 6px;font-size:12px}
  button.remove:hover{background:var(--err-bg);border-color:var(--err-bd)}
  .empty{color:var(--muted);font-size:12.5px;margin:0 0 4px}
</style>
</head>
<body>
<header>
  <span class="brand-mark" aria-hidden="true">GA</span>
  <span class="brand-copy"><b>Git Airlock</b><span>Repositories</span></span>
</header>
<main>
  <h1>Repositories</h1>
  <p class="sub">Repositories watched by this dashboard and the scheduled worker.</p>
  <div class="msg">{{MSG}}</div>
  <div class="card">
    <label>Configured repositories</label>
    {{LIST}}
    <div class="actions">
      <button id="add">Add config…</button>
    </div>
    <div class="actions end">
      {{CANCEL}}
      <button id="start" class="primary">Save and open dashboard</button>
    </div>
    <div id="status" class="status"></div>
    <p class="hint">The scheduled worker watches the same list. Create a config with <code>airlock init</code>.</p>
  </div>
</main>
<script>
(function () {
  var status = document.getElementById('status');
  function say(text, cls) { status.textContent = text || ''; status.className = 'status' + (cls ? ' ' + cls : ''); }
  function call(path, body) {
    return fetch(path, { method: 'POST', body: body || null, headers: { 'Content-Type': 'application/json' } })
      .then(function (r) { if (!r.ok) { throw new Error('HTTP ' + r.status); } return r.json(); });
  }
  document.getElementById('add').addEventListener('click', function () {
    say('Choose a config…');
    call('/_airlock/repos/add').then(function (r) {
      if (r && r.error) { say(r.error, 'err'); return; }
      if (r && r.path) { window.location.reload(); return; }
      say('');
    }).catch(function (e) { say(String(e), 'err'); });
  });
  var buttons = document.querySelectorAll('button.remove');
  for (var i = 0; i < buttons.length; i++) {
    buttons[i].addEventListener('click', function () {
      call('/_airlock/repos/remove?path=' + encodeURIComponent(this.getAttribute('data-path')))
        .then(function () { window.location.reload(); })
        .catch(function (e) { say(String(e), 'err'); });
    });
  }
  document.getElementById('start').addEventListener('click', function () {
    say('Saving…');
    call('/_airlock/start').then(function (r) {
      if (r && r.error) { say(r.error, 'err'); return; }
      say('Starting…', 'ok');
      window.location.replace('/');
    }).catch(function (e) { say(String(e), 'err'); });
  });
  var cancel = document.getElementById('cancel');
  if (cancel) {
    cancel.addEventListener('click', function () {
      call('/_airlock/cancel').then(function () { window.location.replace('/'); })
        .catch(function () { window.location.replace('/'); });
    });
  }
})();
</script>
</body>
</html>
`

// ---------- tray ----------

func (a *App) startTray() {
	// The tray is a Win32 window: Windows delivers its mouse and menu messages
	// only to the thread that created it. Go may move a goroutine between OS
	// threads, and once that happens the message loop sits on a thread that
	// receives nothing, so the icon stays visible but stops answering clicks
	// until the app restarts (upstream getlantern/systray#269). Pin the goroutine.
	goruntime.LockOSThread()
	defer goruntime.UnlockOSThread()

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

// setTooltip updates the icon tooltip. The result watcher can run before the
// tray exists, and Shell_NotifyIcon panics on a nil icon, so the message is
// kept until onReady applies it.
func (a *App) setTooltip(msg string) {
	a.tip.Store(msg)
	if a.tray.Load() {
		systray.SetTooltip(msg)
	}
}

func (a *App) onReady(icon []byte) {
	systray.SetIcon(icon)
	systray.SetTitle("Git Airlock")
	systray.SetTooltip("Git Airlock")
	// Left click opens the dashboard; the context menu stays on right click.
	systray.SetOnTapped(a.showWindow)
	mOpen := systray.AddMenuItem("Open dashboard", "Show the Git Airlock window")
	mRepos := systray.AddMenuItem("Repositories…", "Choose which repositories the dashboard watches")
	mStatus := systray.AddMenuItem("No activity yet", "Latest worker result; click to open the dashboard")
	mSync := systray.AddMenuItem("Sync now", "Sync mirrors with the private remote")
	if a.current.Load() != nil {
		mSync.Enable()
	} else {
		mSync.Disable()
	}
	mUpdates := systray.AddMenuItem("Check for updates…", "Check GitHub for a newer version")
	if version == "dev" {
		mUpdates.Disable()
	}
	mDev := systray.AddMenuItem("Install latest dev build…", "Download and install the latest build from the dev branch")
	mRevert := systray.AddMenuItem("Revert to last stable version…", "Reinstall the latest stable release")
	a.syncItem = mSync
	a.statusItem = mStatus
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Quit Git Airlock")
	a.tray.Store(true)
	if tip, _ := a.tip.Load().(string); tip != "" {
		systray.SetTooltip(tip)
	}

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				a.navigateFromTray("/")
			case <-mRepos.ClickedCh:
				a.navigateFromTray("/repositories")
			case <-mStatus.ClickedCh:
				a.showWindow()
			case <-mSync.ClickedCh:
				go a.syncNow()
			case <-mUpdates.ClickedCh:
				a.showWindow()
				go a.checkUpdates(true)
			case <-mDev.ClickedCh:
				a.showWindow()
				go a.checkDevUpdates()
			case <-mRevert.ClickedCh:
				a.showWindow()
				go a.revertToStable()
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
		a.showWindow()
		a.toast("Sync: no repositories configured.", false, true)
		notify("Git Airlock", "Sync: no repositories configured.")
		return
	}
	a.showWindow()
	a.toast("Syncing mirrors…", true, false)
	a.setTooltip("Git Airlock: syncing…")
	// Tell the user if a sync takes noticeably long; a fast one only reports
	// the result below, so there is no toast spam.
	done := make(chan struct{})
	go func() {
		select {
		case <-time.After(2 * time.Second):
			notify("Git Airlock", "Syncing mirrors…")
		case <-done:
		}
	}()
	failed := false
	var parts []string
	for _, cfg := range box.cfgs {
		report, err := reposync.Sync(context.Background(), cfg)
		if err != nil {
			failed = true
		}
		parts = append(parts, cfg.Repo+": "+syncMessage(report, err))
	}
	close(done)
	msg := join(parts)
	a.setTooltip(truncate("Git Airlock: "+msg, 120))
	a.toast("Sync: "+msg, false, failed)
	notify("Git Airlock", truncate("Sync: "+msg, 200))
}

// toast shows a small banner inside the webview. Manual actions such as Sync
// now must report back even when Windows notifications are disabled.
func (a *App) toast(msg string, sticky, failed bool) {
	if a.ctx == nil {
		return
	}
	bg, bd, fg := "var(--ok-bg,#dcfce7)", "var(--ok-bd,#bbf7d0)", "var(--ok-fg,#166534)"
	if failed {
		bg, bd, fg = "var(--err-bg,#fee2e2)", "var(--err-bd,#fecaca)", "var(--err-fg,#991b1b)"
	}
	css := "position:fixed;right:16px;bottom:42px;z-index:99;max-width:420px;padding:9px 12px;border-radius:8px;" +
		"background:" + bg + ";border:1px solid " + bd + ";color:" + fg + ";" +
		`font:13px/1.4 "Segoe UI",system-ui,sans-serif;box-shadow:0 8px 24px rgba(15,23,42,.16);white-space:pre-wrap;`
	hide := "el.style.display='block';el._timer=setTimeout(function(){el.style.display='none';},5000);"
	if sticky {
		hide = "el.style.display='block';if(el._timer){clearTimeout(el._timer);el._timer=null;}"
	}
	js := fmt.Sprintf(`(function(){try{var id='airlock-toast';var el=document.getElementById(id);if(!el){el=document.createElement('div');el.id=id;document.body.appendChild(el);}el.textContent=%s;el.style.cssText=%s;%s}catch(e){}})()`,
		strconv.Quote(msg), strconv.Quote(css), hide)
	wailsruntime.WindowExecJS(a.ctx, js)
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

func statePath() string {
	if p, err := configlist.DefaultPath(); err == nil {
		return p
	}
	return ""
}

func loadSavedConfigs() []string {
	return configlist.Load(statePath())
}

func saveConfigs(paths []string) error {
	return configlist.Save(statePath(), paths)
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
