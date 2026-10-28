//go:build windows

//go:generate go run github.com/akavel/rsrc@v0.10.2 -manifest app.manifest -o rsrc_windows_amd64.syso

// Command airlock-desktop is the native Windows dashboard for Git Airlock.
// It reads the same config and queue as the CLI; the worker stays responsible
// for syncing and talking to the git host.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/erikibolya/git-airlock/internal/config"
	"github.com/erikibolya/git-airlock/internal/gitutil"
	"github.com/erikibolya/git-airlock/internal/queue"
)

var version = "dev"

type app struct {
	cfg     *config.Config
	cfgPath string
	store   queue.Store

	mw          *walk.MainWindow
	queueTable  *walk.TableView
	resultTable *walk.TableView
	logView     *walk.TextEdit
	statusView  *walk.TextEdit
	statusLabel *walk.Label

	queueModel  *queueModel
	resultModel *resultModel
	refreshing  bool
}

func main() {
	cfgPath, err := resolveConfig()
	if err != nil {
		walk.MsgBox(nil, "Git Airlock", err.Error(), walk.MsgBoxIconError)
		os.Exit(1)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		walk.MsgBox(nil, "Git Airlock", err.Error(), walk.MsgBoxIconError)
		os.Exit(1)
	}
	store, err := queue.Open(cfg.Queue, cfg.Mirror)
	if err != nil {
		walk.MsgBox(nil, "Git Airlock", err.Error(), walk.MsgBoxIconError)
		os.Exit(1)
	}
	if err := store.Ensure(); err != nil {
		walk.MsgBox(nil, "Git Airlock", err.Error(), walk.MsgBoxIconError)
		os.Exit(1)
	}

	a := &app{cfg: cfg, cfgPath: cfgPath, store: store, queueModel: &queueModel{}, resultModel: &resultModel{}}
	if err := a.createWindow(); err != nil {
		walk.MsgBox(nil, "Git Airlock", err.Error(), walk.MsgBoxIconError)
		os.Exit(1)
	}
	_ = saveConfigPath(cfgPath)
	a.refresh()
	go a.loop()
	a.mw.Run()
}

func (a *app) createWindow() error {
	return (MainWindow{
		AssignTo: &a.mw,
		Title:    fmt.Sprintf("Git Airlock %s — %s", version, a.cfg.Repo),
		MinSize:  Size{Width: 980, Height: 620},
		Size:     Size{Width: 1120, Height: 720},
		Layout:   VBox{},
		Children: []Widget{
			TabWidget{
				Pages: []TabPage{
					{
						Title:  "Queue",
						Layout: VBox{},
						Children: []Widget{
							Composite{Layout: HBox{}, Children: []Widget{
								PushButton{Text: "Refresh", OnClicked: func() { a.refresh() }},
								PushButton{Text: "Open PR…", OnClicked: func() { a.openPR() }},
								PushButton{Text: "Check PR…", OnClicked: func() { a.checkPR() }},
								PushButton{Text: "Merge PR…", OnClicked: func() { a.mergePR() }},
								HSpacer{},
							}},
							TableView{
								AssignTo:         &a.queueTable,
								Model:            a.queueModel,
								AlternatingRowBG: true,
								Columns: []TableViewColumn{
									{Title: "ID", Width: 175},
									{Title: "Action", Width: 80},
									{Title: "Details", Width: 420},
									{Title: "Merge", Width: 130},
								},
							},
						},
					},
					{
						Title:  "Results",
						Layout: VBox{},
						Children: []Widget{
							Composite{Layout: HBox{}, Children: []Widget{
								PushButton{Text: "Refresh", OnClicked: func() { a.refresh() }},
								PushButton{Text: "Open PR in browser", OnClicked: func() { a.openSelectedResult() }},
								HSpacer{},
							}},
							TableView{
								AssignTo:         &a.resultTable,
								Model:            a.resultModel,
								AlternatingRowBG: true,
								OnItemActivated:  func() { a.openSelectedResult() },
								Columns: []TableViewColumn{
									{Title: "ID", Width: 175},
									{Title: "Action", Width: 75},
									{Title: "Status", Width: 85},
									{Title: "PR", Width: 60},
									{Title: "Message", Width: 600},
								},
							},
						},
					},
					{
						Title:  "Logs",
						Layout: VBox{},
						Children: []Widget{
							Composite{Layout: HBox{}, Children: []Widget{
								PushButton{Text: "Refresh", OnClicked: func() { a.refresh() }},
								PushButton{Text: "Open log file", OnClicked: func() { a.openLogFile() }},
								HSpacer{},
							}},
							TextEdit{AssignTo: &a.logView, ReadOnly: true, VScroll: true},
						},
					},
					{
						Title:  "Status",
						Layout: VBox{},
						Children: []Widget{
							TextEdit{AssignTo: &a.statusView, ReadOnly: true, VScroll: true, MinSize: Size{Height: 180}},
							Composite{Layout: HBox{}, Children: []Widget{
								PushButton{Text: "Sync mirror now", OnClicked: func() { a.syncNow() }},
								PushButton{Text: "Open config folder", OnClicked: func() { a.openConfigFolder() }},
								PushButton{Text: "Change config…", OnClicked: func() { a.changeConfig() }},
								HSpacer{},
							}},
							Label{Text: "The scheduled worker performs syncs and API calls. This window only reads the queue and enqueues requests."},
						},
					},
				},
			},
			Composite{Layout: HBox{}, Children: []Widget{
				Label{AssignTo: &a.statusLabel, Text: "starting…"},
				HSpacer{},
				PushButton{Text: "Quit", OnClicked: func() { a.mw.Close() }},
			}},
		},
	}).Create()
}

func (a *app) loop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		a.mw.Synchronize(func() { a.refresh() })
	}
}

func (a *app) refresh() {
	if a.refreshing {
		return
	}
	a.refreshing = true
	defer func() { a.refreshing = false }()

	pending, err := a.store.Pending()
	if err != nil {
		a.setStatus("queue: " + err.Error())
		return
	}
	results, err := a.store.ListResults(100)
	if err != nil {
		a.setStatus("results: " + err.Error())
		return
	}
	logs, err := a.store.TailLog(300)
	if err != nil {
		logs = "log: " + err.Error()
	}
	a.queueModel.Set(pending)
	a.resultModel.Set(results)
	_ = a.logView.SetText(logs)
	_ = a.statusView.SetText(a.statusText())
	a.setStatus(fmt.Sprintf("%d pending · %d results · %s", len(pending), len(results), time.Now().Format("15:04:05")))
}

func (a *app) statusText() string {
	return fmt.Sprintf(`repo:     %s
provider: %s
remote:   %s
mirror:   %s
queue:    %s
base:     %s
merge:    %s
config:   %s

The scheduled worker runs the sync and the API calls.
This window only reads the queue and enqueues requests.
`, a.cfg.Repo, a.cfg.Provider, a.cfg.Remote, a.cfg.Mirror, a.cfg.Queue, a.cfg.Base, a.cfg.Merge, a.cfgPath)
}

func (a *app) setStatus(s string) {
	if a.statusLabel != nil {
		_ = a.statusLabel.SetText(s)
	}
}

// ---------- actions ----------

func (a *app) openPR() {
	var dlg *walk.Dialog
	var headEdit, baseEdit, titleEdit *walk.LineEdit
	var bodyEdit *walk.TextEdit
	var mergeCombo *walk.ComboBox
	var dryCheck *walk.CheckBox

	merges := []string{"when-green", "never", "now"}
	idx := 0
	switch a.cfg.Merge {
	case "never":
		idx = 1
	case "now":
		idx = 2
	}
	err := (Dialog{
		AssignTo: &dlg,
		Title:    "Open pull request",
		MinSize:  Size{Width: 560, Height: 500},
		Layout:   VBox{},
		Children: []Widget{
			Label{Text: "Head branch"},
			LineEdit{AssignTo: &headEdit},
			Label{Text: "Base branch"},
			LineEdit{AssignTo: &baseEdit, Text: a.cfg.Base},
			Label{Text: "Title (optional)"},
			LineEdit{AssignTo: &titleEdit},
			Label{Text: "Body (optional)"},
			TextEdit{AssignTo: &bodyEdit, MinSize: Size{Height: 100}},
			Label{Text: "Merge"},
			ComboBox{AssignTo: &mergeCombo, Model: merges, CurrentIndex: idx},
			CheckBox{AssignTo: &dryCheck, Text: "Dry run only (do not create the PR)"},
			Composite{Layout: HBox{}, Children: []Widget{
				HSpacer{},
				PushButton{Text: "Queue", OnClicked: func() {
					head := strings.TrimSpace(headEdit.Text())
					base := strings.TrimSpace(baseEdit.Text())
					if head == "" || base == "" || head == base {
						walk.MsgBox(dlg, "Git Airlock", "Head and base must be non-empty and differ.", walk.MsgBoxIconWarning)
						return
					}
					merge := merges[mergeCombo.CurrentIndex()]
					headSHA := ""
					if out, err := exec.Command("git", "-C", a.cfg.Mirror, "rev-parse", "refs/heads/"+head).Output(); err == nil {
						headSHA = strings.TrimSpace(string(out))
					}
					id, err := a.store.Enqueue(&queue.Request{
						Action:  queue.ActionOpen,
						Head:    head,
						HeadSHA: headSHA,
						Base:    base,
						Title:   strings.TrimSpace(titleEdit.Text()),
						Body:    bodyEdit.Text(),
						Merge:   merge,
						DryRun:  dryCheck.Checked(),
					})
					if err != nil {
						walk.MsgBox(dlg, "Git Airlock", err.Error(), walk.MsgBoxIconError)
						return
					}
					dlg.Accept()
					a.refresh()
					a.setStatus("queued " + id)
				}},
				PushButton{Text: "Cancel", OnClicked: func() { dlg.Cancel() }},
			}},
		},
	}).Create(a.mw)
	if err != nil {
		walk.MsgBox(a.mw, "Git Airlock", err.Error(), walk.MsgBoxIconError)
		return
	}
	dlg.Run()
}

func (a *app) checkPR() {
	var dlg *walk.Dialog
	var numberEdit *walk.LineEdit
	err := (Dialog{
		AssignTo: &dlg,
		Title:    "Check pull request",
		MinSize:  Size{Width: 360, Height: 170},
		Layout:   VBox{},
		Children: []Widget{
			Label{Text: "PR number"},
			LineEdit{AssignTo: &numberEdit},
			Composite{Layout: HBox{}, Children: []Widget{
				HSpacer{},
				PushButton{Text: "Queue", OnClicked: func() {
					number, err := strconv.Atoi(strings.TrimSpace(numberEdit.Text()))
					if err != nil || number <= 0 {
						walk.MsgBox(dlg, "Git Airlock", "Enter a positive PR number.", walk.MsgBoxIconWarning)
						return
					}
					id, err := a.store.Enqueue(&queue.Request{Action: queue.ActionStatus, Number: number})
					if err != nil {
						walk.MsgBox(dlg, "Git Airlock", err.Error(), walk.MsgBoxIconError)
						return
					}
					dlg.Accept()
					a.refresh()
					a.setStatus("queued " + id)
				}},
				PushButton{Text: "Cancel", OnClicked: func() { dlg.Cancel() }},
			}},
		},
	}).Create(a.mw)
	if err != nil {
		walk.MsgBox(a.mw, "Git Airlock", err.Error(), walk.MsgBoxIconError)
		return
	}
	dlg.Run()
}

func (a *app) mergePR() {
	var dlg *walk.Dialog
	var numberEdit *walk.LineEdit
	var whenGreen *walk.CheckBox
	err := (Dialog{
		AssignTo: &dlg,
		Title:    "Merge pull request",
		MinSize:  Size{Width: 400, Height: 200},
		Layout:   VBox{},
		Children: []Widget{
			Label{Text: "PR number"},
			LineEdit{AssignTo: &numberEdit},
			CheckBox{AssignTo: &whenGreen, Text: "Only when checks are green", Checked: true},
			Composite{Layout: HBox{}, Children: []Widget{
				HSpacer{},
				PushButton{Text: "Queue", OnClicked: func() {
					number, err := strconv.Atoi(strings.TrimSpace(numberEdit.Text()))
					if err != nil || number <= 0 {
						walk.MsgBox(dlg, "Git Airlock", "Enter a positive PR number.", walk.MsgBoxIconWarning)
						return
					}
					merge := "now"
					if whenGreen.Checked() {
						merge = "when-green"
					}
					id, err := a.store.Enqueue(&queue.Request{Action: queue.ActionMerge, Number: number, Merge: merge})
					if err != nil {
						walk.MsgBox(dlg, "Git Airlock", err.Error(), walk.MsgBoxIconError)
						return
					}
					dlg.Accept()
					a.refresh()
					a.setStatus("queued " + id)
				}},
				PushButton{Text: "Cancel", OnClicked: func() { dlg.Cancel() }},
			}},
		},
	}).Create(a.mw)
	if err != nil {
		walk.MsgBox(a.mw, "Git Airlock", err.Error(), walk.MsgBoxIconError)
		return
	}
	dlg.Run()
}

func (a *app) syncNow() {
	a.setStatus("syncing…")
	go func() {
		report, err := gitutil.Sync(context.Background(), a.cfg.Mirror, a.cfg.Remote)
		msg := syncMessage(report, err)
		a.mw.Synchronize(func() {
			a.setStatus(msg)
			a.refresh()
			walk.MsgBox(a.mw, "Git Airlock sync", msg, walk.MsgBoxIconInformation)
		})
	}()
}

func (a *app) openSelectedResult() {
	idx := a.resultTable.CurrentIndex()
	if idx < 0 || idx >= len(a.resultModel.items) {
		a.setStatus("select a result first")
		return
	}
	url := a.resultModel.items[idx].URL
	if url == "" {
		a.setStatus("that result has no PR URL")
		return
	}
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func (a *app) openLogFile() {
	path := a.store.LogPath()
	if path == "" {
		a.setStatus("this queue has no log file")
		return
	}
	_ = exec.Command("notepad.exe", path).Start()
}

func (a *app) openConfigFolder() {
	_ = exec.Command("explorer.exe", "/select,"+a.cfgPath).Start()
}

func (a *app) changeConfig() {
	fd := walk.FileDialog{
		Title:  "Choose .airlock.toml",
		Filter: "Airlock config (*.toml)|*.toml|All files (*.*)|*.*",
	}
	ok, err := fd.ShowOpen(a.mw)
	if err != nil || !ok {
		return
	}
	cfg, err := config.Load(fd.FilePath)
	if err != nil {
		walk.MsgBox(a.mw, "Git Airlock", err.Error(), walk.MsgBoxIconError)
		return
	}
	store, err := queue.Open(cfg.Queue, cfg.Mirror)
	if err != nil {
		walk.MsgBox(a.mw, "Git Airlock", err.Error(), walk.MsgBoxIconError)
		return
	}
	if err := store.Ensure(); err != nil {
		walk.MsgBox(a.mw, "Git Airlock", err.Error(), walk.MsgBoxIconError)
		return
	}
	a.cfg = cfg
	a.cfgPath = fd.FilePath
	a.store = store
	_ = a.mw.SetTitle(fmt.Sprintf("Git Airlock %s — %s", version, cfg.Repo))
	_ = saveConfigPath(fd.FilePath)
	a.refresh()
}

func syncMessage(r gitutil.SyncReport, err error) string {
	var parts []string
	if len(r.Pushed) > 0 {
		parts = append(parts, "pushed "+strings.Join(r.Pushed, ", "))
	}
	if len(r.FastForwarded) > 0 {
		parts = append(parts, "fast-forwarded "+strings.Join(r.FastForwarded, ", "))
	}
	if len(r.Diverged) > 0 {
		parts = append(parts, "DIVERGED "+strings.Join(r.Diverged, ", "))
	}
	if err != nil {
		parts = append(parts, "error: "+err.Error())
	} else if len(parts) == 0 {
		parts = append(parts, "already up to date")
	}
	return "sync: " + strings.Join(parts, "; ")
}

// ---------- config path ----------

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

func resolveConfig() (string, error) {
	if p := argValue("--config"); p != "" {
		return p, nil
	}
	if p := loadSavedConfig(); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := config.Resolve(""); err == nil {
		return p, nil
	}
	return "", fmt.Errorf(`no .airlock.toml found.

Start the app with an explicit config, for example:

  airlock-desktop.exe --config Z:\path\to\.airlock.windows.toml

or create a shortcut with that argument. The chosen path is remembered.`)
}

func argValue(name string) string {
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == name && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
		if strings.HasPrefix(os.Args[i], name+"=") {
			return strings.TrimPrefix(os.Args[i], name+"=")
		}
	}
	return ""
}

// ---------- table models ----------

type queueModel struct {
	walk.TableModelBase
	items []queue.Request
}

func (m *queueModel) RowCount() int { return len(m.items) }

func (m *queueModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.items) {
		return ""
	}
	r := m.items[row]
	switch col {
	case 0:
		return r.ID
	case 1:
		return r.Action
	case 2:
		if r.Action == queue.ActionOpen {
			return r.Head + " → " + r.Base
		}
		if r.Number > 0 {
			return fmt.Sprintf("#%d", r.Number)
		}
		return ""
	case 3:
		if r.Merge == "" {
			return "—"
		}
		if r.DryRun {
			return r.Merge + " (dry)"
		}
		return r.Merge
	}
	return ""
}

func (m *queueModel) Set(items []queue.Request) {
	m.items = items
	m.PublishRowsReset()
}

type resultModel struct {
	walk.TableModelBase
	items []queue.Result
}

func (m *resultModel) RowCount() int { return len(m.items) }

func (m *resultModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.items) {
		return ""
	}
	r := m.items[row]
	switch col {
	case 0:
		return r.ID
	case 1:
		return r.Action
	case 2:
		if r.OK {
			return "ok"
		}
		if r.Pending {
			return "pending"
		}
		return "failed"
	case 3:
		if r.PR > 0 {
			return fmt.Sprintf("#%d", r.PR)
		}
		return ""
	case 4:
		return r.Message
	}
	return ""
}

func (m *resultModel) Set(items []queue.Result) {
	m.items = items
	m.PublishRowsReset()
}
