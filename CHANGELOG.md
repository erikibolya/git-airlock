# Changelog

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
