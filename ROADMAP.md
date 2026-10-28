# Roadmap

## Shipped

- **v0.1** — mirror sync, file queue, Gitea and GitHub PR automation.
- **v0.2** — GitLab adapter, git-ref queue (`refs/airlock/*`), release workflow
  with Linux/macOS/Windows binaries.
- **v0.3** — local web dashboard (loopback-first, optional token-protected LAN).
- **v0.4** — native Windows desktop dashboard.
- **v0.5** — multi-repository worker (`--config` repeatable), terminal status
  command (`airlock status --watch`), duplicate-PR detection for merged/closed
  requests, `HeadSHA` in requests.
- **v0.6** — remote queue over SSH (`queue_remote`) with a local cache; no
  shared queue folder required.
- **v0.6.3** — hide console windows for Git children spawned by the Windows GUI
  (no flashing `cmd` windows, no focus stealing).
- **v0.7** — cross-platform desktop app (Wails: WebView2 / WebKitGTK / WKWebView)
  with a notification-area icon, close-to-tray, single-instance activation and a
  Sync-now action. A Windows installer sets up the CLI, the desktop app, the
  autostart entry and the worker task in one go. The old Win32 dashboard is
  replaced.
- **v0.8** — automatic pull requests: `auto_pr = true` makes the worker open a
  PR for every branch pushed to the mirror and merge it when checks are green.
  No explicit `airlock pr open` is needed; existing open PRs and closed/merged
  ones are left alone.
- **v0.8.1** — status-first dashboard: an overview with the worker state, the
  watched repository, in-progress checks and recent activity; manual request
  forms moved to their own page.
- **v0.9** — multi-repository dashboards: the default page lists every watched
  repository (state, pending count, last activity, auto PR, base) and each row
  opens a detail page. `airlock ui` accepts repeated `--config`; the desktop
  app has a repository picker (tray → Repositories…) that also updates the
  scheduled worker to the same list.
- **v1.0** — stability release: per-repository worker logs, multi-repository
  `airlock status`, cross-platform worker-task updates from the desktop picker,
  changelog and documentation. No breaking changes since 0.9.
- **v1.1** — merge strategies (`merge`, `squash`, `rebase`) in the config, the
  CLI and the dashboard, used by `auto_pr` too; the desktop tray menu shows the
  latest worker result.
- **v1.2** — optional branch cleanup after a merge (`delete_branch`,
  `--delete-branch`, dashboard checkboxes).

## Next

### MCP server

`airlock mcp` would run a stdio MCP server exposing `pr_open`, `pr_status`,
`pr_merge` and `sync` as tools. It would wrap the same queue as the CLI, so the
worker model stays unchanged, but agents get structured input/output instead of
shell quoting. The current interface is the CLI plus the `AGENTS.md` snippet;
the MCP server is not implemented yet.

### Sync over SSH

The queue can already travel over SSH; the commit mirror used by `sync` still
needs a local path/clone. Add a managed mirror cache so the whole flow can use
`ssh://` for both commits and queue refs.

### Notifications

The tray menu and tooltip show the latest result. Native desktop toasts are not
shipped yet; Windows needs a registered AppUserModelID/shortcut for unpackaged
apps, which should be added together with the installer shortcut.

### Labels and review automation

Optional labels (`airlock:auto-merge`), reviewers, and a "merge only when
approved" policy.

### Packaging

The Windows installer covers the direct-download case (CLI, desktop app,
autostart, worker task). Homebrew, Scoop/winget and Linux packages are still
missing.

### Integration tests

Docker Compose with Gitea + act_runner + two airlock peers (isolated client and
worker) to exercise the full PR/check/merge path in CI.
