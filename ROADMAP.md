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

## Next

### Sync over SSH

The queue can already travel over SSH; the commit mirror used by `sync` still
needs a local path/clone. Add a managed mirror cache so the whole flow can use
`ssh://` for both commits and queue refs.

### Multi-repository dashboards

The worker supports many configs; the desktop and web dashboards still show one
repository at a time (Change config). Add a config list and a repository
selector.

### Notifications

Desktop tray icon with a balloon/toast when a request finishes (merged, failed,
checks pending) and a tray menu for Sync / Open dashboard.

### Merge strategies

`--merge squash|rebase|merge` per request for GitHub/GitLab/Gitea, plus optional
branch deletion after merge.

### Labels and review automation

Optional labels (`airlock:auto-merge`), reviewers, and a "merge only when
approved" policy.

### Packaging

Homebrew, Scoop/winget and prebuilt archives are partially covered by GitHub
Releases; add package-manager manifests.

### Integration tests

Docker Compose with Gitea + act_runner + two airlock peers (isolated client and
worker) to exercise the full PR/check/merge path in CI.
