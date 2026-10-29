# Changelog

## 1.8.5 — 2026-10-29

- **Sync now** brings the window forward and shows an in-app banner
  ("Syncing mirrors…" and then the result or error), so feedback no longer
  depends on Windows notification settings. The native toast remains as a
  background fallback.
- The Logs page uses the whole window: the log panel has no inner scrollbar,
  long lines wrap and the page itself scrolls.

## 1.8.4 — 2026-10-29

- The tray menu has **Check for updates…**, matching **Help → Check for
  updates…**. It shows the window and runs the same signed-manifest check.

## 1.8.3 — 2026-10-29

- Removed the **Manual** page and its File menu entry. The dashboard is
  read-only: use `airlock pr open|status|merge` for exceptions, auto PR for the
  normal path, and the tray/File **Sync now** (or `airlock sync`) for an
  immediate sync. `/manual` redirects to the repository overview.
- Removed the dashboard's form POST endpoints (`/pr/open`, `/pr/status`,
  `/pr/merge`, `/sync`) and CSRF handling, so the web UI can no longer mutate
  the queue. The result, logs and repository pages remain.

## 1.8.2 — 2026-10-29

- The repository picker edits a draft list: **Add config…** and **remove** no
  longer touch the saved list until **Save and open dashboard**, and **Cancel**
  restores the running repositories without changing the scheduled worker.
- The picker's primary action is now labelled **Save and open dashboard**, so it
  is clear that it also re-registers the worker task.

## 1.8.1 — 2026-10-29

- Faster page switches: the dashboard caches the queue summary for three
  seconds, uses the activity list instead of reading one result per pending
  request, and no longer reloads an idle dashboard every 60 seconds.
- The desktop window hides the in-page navigation. Dashboard, Repositories,
  Manual and Logs live in the native File menu, so they are not duplicated in
  the header.
- **Open dashboard** in the repository picker no longer flashes a console:
  `schtasks` runs with `CREATE_NO_WINDOW`.
- **Sync now** from the native menu or the tray reports the result in a native
  notification, with a "syncing" notification if it takes more than two seconds.
- The manual page is grouped into Pull requests, Maintenance, Pending requests
  and Recent results, with a proper page title.

## 1.8.0 — 2026-10-29

- Native-looking redesign of the dashboard and repository picker: 13px Segoe UI,
  the light/dark palette from RedLine, a compact 52px header, flat 8px panels,
  status pills and badges, restrained form controls and a 30px status bar. The
  page layout is unchanged.
- Show the window before executing navigation JS; WebView2 can drop
  ExecuteScript while hidden, which made tray **Repositories…** look like it
  opened the dashboard.

## 1.7.4 — 2026-10-29

- The Windows worker runs from `airlock-worker.exe`, a GUI-subsystem build of
  the same code. Task Scheduler starts it without ever creating a console
  window; portable installs without that file fall back to
  `airlock.exe worker --hidden`, so nothing flashes every minute.
- The tray's **Repositories…** entry now opens the repository picker instead of
  showing the dashboard again, and **Open dashboard** always returns to the
  dashboard.
- The native menu is consolidated into **File** (Dashboard, Repositories…,
  Manual, Logs, Sync now, Quit) and **Help**. The confusing **View** submenu is
  gone.

## 1.7.3 — 2026-10-29

- Windows builds carry PE metadata: `installer/winres.json` is rendered by
  `go-winres` for `airlock.exe` (company, product, file description, version,
  icon, manifest) and the desktop `info` block in `desktop/wails.json` is stamped
  from the release tag. Windows binaries also keep their symbols. All of this
  makes the executables look less like anonymous malware to Defender's ML
  heuristics, which have been quarantining the unsigned worker as
  `Trojan:Win32/Bearfoos.B!ml` (false positive).
- The Windows scheduled task starts `airlock.exe worker --hidden` directly
  instead of going through `worker.vbs`. The worker hides its own console, so
  there is no console flash and no Windows Script Host error dialog every
  minute; upgrades remove the old launcher. The task definition no longer
  contains a hidden script + scheduled task pattern for AV heuristics to score.
- Added a code signing policy and the SignPath Foundation setup guide in
  [docs/CODE_SIGNING.md](docs/CODE_SIGNING.md); the application was submitted on
  2026-10-29 and releases stay unsigned until it is approved.
- Added an antivirus troubleshooting section to the README.

## 1.7.2 — 2026-10-29

- The desktop tray icon no longer goes dead. The systray message loop was started
  in a goroutine the Go scheduler is free to move between OS threads; Windows
  delivers the icon's clicks to the thread that created it, so after a migration
  the icon stayed visible but left and right clicks were ignored until the app
  restarted. The tray goroutine is now pinned to one thread with
  `runtime.LockOSThread()`. Tooltip updates are also held back until the icon
  exists instead of racing tray start-up.

## 1.7.1 — 2026-10-28

- Fix the repository picker: **Open dashboard**, **Add config…**, **remove** and
  **Cancel** used Wails bindings (`window.go`), which do not exist in the
  server-rendered picker, so they failed with "Native bindings are still
  loading". The buttons now call small same-origin endpoints under
  `/_airlock/`.

## 1.7.0 — 2026-10-28

- Signed in-app updates. Releases publish `latest.json` with the Windows
  installer URL, SHA256 and an Ed25519 signature. The desktop app checks it on
  startup and via **Help → Check for updates…**, offers Install now / Later,
  verifies the signature and checksum, runs the installer in silent mode and
  restarts itself. `airlock-desktop --check-updates` is a headless diagnostic.
- The desktop version is embedded by the build (`-X main.version`).

## 1.6.3 — 2026-10-28

- Dashboard pages read queue results with a single `git cat-file --batch`
  process instead of one per result. On SMB/NFS mirrors this cuts page loads
  from seconds to a fraction of a second.
- The auto-refresh interval is 60 seconds instead of 15, so it no longer fights
  with navigation.
- Native-menu navigation uses `WindowExecJS` (a direct location change) instead
  of a full window reload, which was slow with WebView2.

## 1.6.2 — 2026-10-28

- A native application menu in the desktop window: File (Dashboard,
  Repositories…, Sync now, Quit), View (Manual, Logs) and Help (GitHub, About).
  All tray actions are available in the app itself; the tray remains for
  background use.

## 1.6.1 — 2026-10-28

- The dashboard header has a **repositories** link. In the desktop app it opens
  the native repository picker (add/remove configs), so adding a repository no
  longer requires finding the tray menu. The web dashboard shows how to pass
  multiple `--config` flags.

## 1.6.0 — 2026-10-28

- Packaging: Homebrew formula (tap `erikibolya/homebrew-git-airlock`), Scoop
  manifest (bucket `erikibolya/scoop-git-airlock`) and winget manifests for
  `ErikIbolya.GitAirlock`, rendered from release assets by
  `packaging/render.py`.
- The integration test now runs workflow jobs in Docker containers: a
  `docker:27-dind` service, a shared workspace volume and act_runner with
  `container.docker_host`. The job executes `node --version` in the
  `node:20-bookworm` container before the merge.
- `.gitattributes` keeps shell scripts and manifests LF on Windows checkouts.

## 1.5.0 — 2026-10-28

- Sync over SSH: `mirror_remote` and `remote_url` let a worker with no shared
  mount keep a local cache, fetch both the isolated mirror and the private
  remote, and fast-forward them without force-pushes. `queue_remote` already
  carries the queue refs over SSH.
- The Docker integration test now uses a real Gitea Actions `act_runner` (host
  mode) and the workflow's `(pull_request)` commit status instead of a simulated
  status.

## 1.4.0 — 2026-10-28

- Native notifications when a request is merged or fails: Windows toast (the
  installer sets the AppUserModelID on the Start-menu shortcut), `notify-send`
  on Linux and the Notification Center on macOS. `airlock-desktop.exe
  --notify-test` shows a test notification.
- The tray menu and tooltip continue to show the latest result.
- Docker integration test: Gitea + a real worker, isolated push, simulated CI
  status, squash merge and branch cleanup (`test/integration/run.sh`).

## 1.3.0 — 2026-10-28

- `airlock mcp`: a Model Context Protocol server over stdio with
  `airlock_status`, `airlock_sync`, `airlock_pr_open`, `airlock_pr_status` and
  `airlock_pr_merge`. It writes only to the queue; the worker performs the
  provider calls.
- New `sync` queue action, so `airlock_sync` (and any future UI) can ask the
  worker to sync immediately.

## 1.2.0 — 2026-10-28

- Branch cleanup: `delete_branch = true` in the config, `--delete-branch` on
  `pr open`/`pr merge`, and checkboxes in the manual forms remove the source
  branch after a successful merge. Cleanup errors are reported but never fail
  the merge.
- The provider API now exposes the PR source branch and a branch delete call
  (Gitea, GitHub, GitLab).

## 1.1.0 — 2026-10-28

- Merge strategies: `merge_strategy = "merge" | "squash" | "rebase"` in the
  config, `--strategy` on `pr open`/`pr merge`, and selectors in the manual
  dashboard forms. `auto_pr` uses the configured strategy. Gitea and GitHub
  support all three; GitLab supports merge and squash.
- The desktop tray menu shows the latest worker result and the tooltip follows
  it (refreshed every 30 seconds); clicking the entry opens the dashboard.

## 1.0.0 — 2026-10-28

Stability release. No breaking changes since 0.9.

- Worker logs are per repository (`worker-<name>-<hash>.log` in the user cache
  directory), so the dashboard, `airlock status` and `logs` no longer mix
  entries from different mirrors.
- `airlock status` accepts repeated `--config` and prints every repository;
  `--json` returns an array for multiple repositories.
- The desktop repository picker updates the scheduled worker on Windows, Linux
  and macOS when the `airlock` binary ships next to the app (it was
  Windows-only).
- Changelog and documentation updates for the 1.0 surface.

## 0.9.0

- Multi-repository dashboard: the landing page lists every watched repository
  (state, pending, last activity, last check, auto PR, base) and each row opens
  the per-repository detail page.
- `airlock ui` accepts repeated `--config`.
- Desktop: tray → **Repositories…** / `--repos` adds and removes configs,
  migrates the old single-config state, and points the worker task at the same
  list without a restart.

## 0.8.1

- Status-first dashboard: state card with an activity bar, pending checks, last
  check and recent activity; manual request forms moved to a separate page.

## 0.8.0

- Automatic pull requests (`auto_pr = true`): the worker opens a PR for every
  branch ahead of base and merges it when checks are green. Existing open PRs,
  closed PRs and merged commits are respected; `auto_pr_ignore` skips patterns.

## 0.7.1 / 0.7.0

- Cross-platform desktop app built with Wails (WebView2, WebKitGTK, WKWebView)
  with a notification-area icon, close-to-tray, single-instance activation and
  Sync now.
- Windows installer: CLI, desktop app, shortcuts, autostart and the scheduled
  worker in one setup.
- The Win32 `lxn/walk` dashboard was removed.

## 0.6.x

- `queue_remote` carries the queue refs over SSH.
- Hidden child processes for the Windows GUI (no console flashes).

## 0.5.0

- Multi-config worker, `airlock status`, duplicate-PR detection and `HeadSHA`.

## 0.4.0

- Native Windows dashboard.

## 0.3.0

- Loopback-first web dashboard with an optional access token.

## 0.2.0

- GitLab provider, git-ref queue, cross-platform release binaries.

## 0.1.0

- Mirror sync, file queue, Gitea and GitHub pull-request automation.
