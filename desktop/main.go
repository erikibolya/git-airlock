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
	"sync"
	"sync/atomic"
	"time"

	"github.com/getlantern/systray"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/erikibolya/git-airlock/internal/config"
	"github.com/erikibolya/git-airlock/internal/gitutil"
	"github.com/erikibolya/git-airlock/internal/ui"
)

//go:embed tray.ico tray.png
var trayFS embed.FS

// handlerBox lets the setup page swap in the real dashboard without a restart.
type handlerBox struct{ h http.Handler }

func main() {
	cfgPath := argValue("--config")
	if cfgPath == "" {
		cfgPath = loadSavedConfig()
	}
	if cfgPath == "" {
		if p, err := config.Resolve(""); err == nil {
			cfgPath = p
		}
	}
	app := &App{
		cfgPath:     cfgPath,
		startHidden: hasFlag("--tray") || hasFlag("--minimized"),
	}
	app.reload()
	if app.cfg != nil && cfgPath != "" {
		// Remember only configs that actually load, so a bad --config does not
		// overwrite a working choice.
		_ = saveConfigPath(cfgPath)
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

// App is the Wails application: it serves the existing web dashboard and adds
// a tray icon, a setup screen, and a native sync action.
type App struct {
	cfgPath     string
	startHidden bool

	cfg   *config.Config
	srv   *ui.Server
	loadE string

	handler atomic.Pointer[handlerBox]
	ctx     context.Context
	quitting atomic.Bool
	tray     atomic.Bool
	syncMu   sync.Mutex
	syncItem *systray.MenuItem
}

// ServeHTTP is the Wails asset handler. It forwards to the setup screen until a
// valid config is available, then to the dashboard.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if hb := a.handler.Load(); hb != nil {
		hb.h.ServeHTTP(w, r)
		return
	}
	a.serveSetup(w, r)
}

func (a *App) setHandler(h http.Handler) {
	a.handler.Store(&handlerBox{h: h})
}

// reload validates the current config (if any) and installs the matching
// handler. A failed reload leaves the setup screen in place.
func (a *App) reload() {
	a.cfg, a.srv, a.loadE = nil, nil, ""
	if a.cfgPath == "" {
		a.loadE = "No .airlock.toml selected yet."
		return
	}
	cfg, err := config.Load(a.cfgPath)
	if err != nil {
		a.loadE = err.Error()
		return
	}
	srv, err := ui.New(cfg, "")
	if err != nil {
		a.loadE = err.Error()
		return
	}
	a.cfg, a.srv = cfg, srv
	a.setHandler(srv.Handler())
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if a.cfg == nil {
		// An autostarted app must not hide a broken configuration in the tray.
		wailsruntime.WindowShow(ctx)
	}
	go a.startTray()
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

// ---------- setup screen ----------

// PickResult is returned to the setup page.
type PickResult struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// PickConfig opens a native file dialog for an .airlock.toml.
func (a *App) PickConfig() PickResult {
	dir := ""
	if a.cfgPath != "" {
		dir = filepath.Dir(a.cfgPath)
	}
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
	return PickResult{Path: path}
}

// ApplyConfig validates the chosen config, saves it for the next launch and
// swaps the dashboard in without restarting.
func (a *App) ApplyConfig(path string) PickResult {
	if path == "" {
		return PickResult{Error: "Enter a config path or use Browse."}
	}
	if cfg, err := config.Load(path); err != nil {
		return PickResult{Error: err.Error()}
	} else if _, err := ui.New(cfg, ""); err != nil {
		return PickResult{Error: err.Error()}
	}
	a.cfgPath = path
	a.reload()
	if a.cfg == nil {
		return PickResult{Error: a.loadE}
	}
	if err := saveConfigPath(path); err != nil {
		return PickResult{Error: err.Error()}
	}
	if a.syncItem != nil {
		a.syncItem.Enable()
	}
	return PickResult{Path: path}
}

func (a *App) serveSetup(w http.ResponseWriter, _ *http.Request) {
	msg := "Select the .airlock.toml that describes this repository."
	if a.loadE != "" {
		msg = a.loadE
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, setupHTML, html.EscapeString(msg), html.EscapeString(a.cfgPath))
}

const setupHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Git Airlock — setup</title>
<style>
  :root { color-scheme: light; }
  body { margin:0; padding:0; font:15px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif;
         background:#f6f8fb; color:#0f172a; }
  main { max-width:720px; margin:0 auto; padding:48px 24px; }
  h1 { font-size:26px; margin:0 0 4px; }
  .sub { color:#475569; margin:0 0 28px; }
  .card { background:#fff; border:1px solid #e2e8f0; border-radius:14px; padding:24px;
          box-shadow:0 1px 2px rgba(15,23,42,.05); }
  label { display:block; font-weight:600; font-size:13px; margin-bottom:6px; }
  .row { display:flex; gap:8px; }
  input[type=text] { flex:1; padding:10px 12px; border:1px solid #cbd5e1; border-radius:8px;
                     font:13px/1.4 ui-monospace, "Cascadia Code", monospace; background:#f8fafc; }
  input[type=text]:focus { outline:2px solid #2563eb33; border-color:#2563eb; background:#fff; }
  button { padding:10px 16px; border-radius:8px; border:1px solid #cbd5e1; background:#fff;
           font:600 13px system-ui; cursor:pointer; }
  button:hover { background:#f1f5f9; }
  button.primary { background:#2563eb; border-color:#2563eb; color:#fff; }
  button.primary:hover { background:#1d4ed8; }
  .actions { display:flex; justify-content:flex-end; gap:8px; margin-top:16px; }
  .status { margin-top:14px; font-size:13px; min-height:20px; }
  .status.err { color:#b91c1c; }
  .status.ok { color:#047857; }
  .hint { margin-top:22px; font-size:13px; color:#64748b; }
  code { background:#f1f5f9; padding:1px 5px; border-radius:4px; }
  .msg { background:#fff7ed; border:1px solid #fed7aa; color:#9a3412; border-radius:10px;
         padding:12px 14px; font-size:13px; margin-bottom:20px; white-space:pre-wrap;
         word-break:break-word; }
</style>
</head>
<body>
<main>
  <h1>Git Airlock</h1>
  <p class="sub">Synchronises a mirror across the air gap and merges pull requests.</p>
  <div class="msg">%s</div>
  <div class="card">
    <label for="path">Configuration file</label>
    <div class="row">
      <input id="path" type="text" autofocus spellcheck="false"
             placeholder="C:\path\to\.airlock.toml" value="%s">
      <button id="browse">Browse…</button>
    </div>
    <div class="actions">
      <button id="apply" class="primary">Use this config</button>
    </div>
    <div id="status" class="status"></div>
    <p class="hint">Create a config with <code>airlock init</code>. The path is remembered,
    so you only need this screen once.</p>
  </div>
</main>
<script>
(function () {
  var input = document.getElementById('path');
  var status = document.getElementById('status');
  function say(text, cls) {
    status.textContent = text || '';
    status.className = 'status' + (cls ? ' ' + cls : '');
  }
  function bind() { return window.go && window.go.main && window.go.main.App; }
  document.getElementById('browse').addEventListener('click', function () {
    var app = bind();
    if (!app) { say('Native bindings are still loading, try again.', 'err'); return; }
    app.PickConfig().then(function (r) {
      if (r && r.error) { say(r.error, 'err'); return; }
      if (r && r.path) { input.value = r.path; say(''); }
    }).catch(function (e) { say(String(e), 'err'); });
  });
  document.getElementById('apply').addEventListener('click', function () {
    var app = bind();
    if (!app) { say('Native bindings are still loading, try again.', 'err'); return; }
    say('Checking…');
    app.ApplyConfig(input.value).then(function (r) {
      if (r && r.error) { say(r.error, 'err'); return; }
      say('Configuration loaded.', 'ok');
      setTimeout(function () { window.location.replace('/'); }, 250);
    }).catch(function (e) { say(String(e), 'err'); });
  });
  input.addEventListener('keydown', function (ev) {
    if (ev.key === 'Enter') { document.getElementById('apply').click(); }
  });
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
	mSync := systray.AddMenuItem("Sync now", "Sync the mirror with the private remote")
	if a.cfg != nil {
		mSync.Enable()
	} else {
		mSync.Disable()
	}
	a.syncItem = mSync
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Quit Git Airlock")
	a.tray.Store(true)

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				a.showWindow()
			case <-mSync.ClickedCh:
				go a.syncNow()
			case <-mQuit.ClickedCh:
				a.quitting.Store(true)
				a.tray.Store(false)
				systray.Quit()
				if a.ctx != nil {
					wailsruntime.Quit(a.ctx)
				}
				return
			}
		}
	}()
}

func (a *App) syncNow() {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	if a.cfg == nil {
		return
	}
	systray.SetTooltip("Git Airlock: syncing…")
	report, err := gitutil.Sync(context.Background(), a.cfg.Mirror, a.cfg.Remote)
	msg := syncMessage(report, err)
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
	Config string `json:"config"`
}

func statePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "git-airlock", "desktop.json")
}

func loadSavedConfig() string {
	path := statePath()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var st desktopState
	if json.Unmarshal(data, &st) != nil {
		return ""
	}
	if _, err := os.Stat(st.Config); err != nil {
		return ""
	}
	return st.Config
}

func saveConfigPath(p string) error {
	path := statePath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(desktopState{Config: p}, "", "  ")
	return os.WriteFile(path, data, 0o644)
}

func argValue(name string) string {
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == name && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
		if len(os.Args[i]) > len(name) && os.Args[i][:len(name)+1] == name+"=" {
			return os.Args[i][len(name)+1:]
		}
	}
	return ""
}

func hasFlag(name string) bool {
	for _, a := range os.Args[1:] {
		if a == name {
			return true
		}
	}
	return false
}
