# AGENTS.md

Working notes for coding agents in this repository. Read this before changing
code; the user-facing documentation is in [README.md](README.md) and the release
procedure is in [docs/RELEASING.md](docs/RELEASING.md).

## What this is

Git Airlock moves commits and pull-request actions across a network air gap.

```
 isolated machine (agent, sandbox)          trusted workstation (VPN)
 ─────────────────────────────────          ────────────────────────
 git push ──► bare mirror ────────────────► sync ──► private remote
 PR request ─► queue/requests ────────────► worker ─► Gitea / GitHub API
               queue/results ◄─────────────┘         (create, checks, merge)
```

The isolated machine only writes to the mirror and the queue. The worker on the
VPN-connected workstation holds the API token, performs the sync and the
provider calls, and writes results back. Fast-forward-only sync is a hard rule:
never force-push.

## Repository map

| Path | What lives there |
| --- | --- |
| `cmd/airlock` | CLI entry point and flag parsing |
| `cmd/updatesign` | `keygen` and `manifest` for signed desktop updates |
| `internal/config` | `.airlock.toml` loading and defaults |
| `internal/gitutil` | mirror sync, fast-forward checks, `BranchesAhead`, SSH sync |
| `internal/queue` | file queue and git-ref queue (`refs/airlock/*`) |
| `internal/worker` | request execution, auto-PR, merge strategies, branch cleanup |
| `internal/provider/{gitea,github,gitlab}` | provider adapters |
| `internal/reposync` | transport selection for `airlock sync` |
| `internal/install` | Windows task, systemd timer, launchd agent |
| `internal/ui` | HTML dashboard, templates, per-repo pages |
| `internal/mcp` | stdio MCP server (`airlock mcp`) |
| `internal/update` | manifest fetch, semver compare, Ed25519 sign/verify, download |
| `desktop/` | Wails app (separate Go module, Go 1.25+); tray, picker, native menu, updater |
| `installer/` | Inno Setup script and `build.ps1` |
| `packaging/` | Homebrew/Scoop/winget templates and `render.py` |
| `test/integration/` | Docker Gitea + DinD + real `act_runner` end-to-end test |
| `docs/screenshots/` | Desktop screenshots used by the README |

## Build and test

Root module (Go 1.22+):

```bash
go vet ./...
go test ./...
go build -o airlock ./cmd/airlock
```

Desktop module (Go 1.25+, Windows needs WebView2, `wails` on PATH):

```bash
cd desktop
go vet ./...
wails build -ldflags "-X main.version=1.7.3"
```

Installer, CLI and desktop in one step (Windows):

```powershell
pwsh -File installer/build.ps1 -Config Z:\repos\Acme\Au.Net\.airlock.windows.toml -Version v1.7.3
```

Integration test (Docker Compose v2):

```bash
bash test/integration/run.sh
```

It starts Gitea, a DinD service and a real `act_runner`, pushes from the
isolated side, runs the worker, waits for checks and asserts the merge and
branch cleanup. It needs network access to pull images.

## Versioning and releases

- Semantic versions, tags are `vX.Y.Z`. Every release updates **CHANGELOG.md**
  and the shipped list in **ROADMAP.md**.
- Bump `installer/git-airlock.iss` (`AppVersion`) and `installer/build.ps1`
  (`$Version`) in the same commit.
- The release workflow publishes `latest.json` next to the installer. The
  manifest is signed with the Ed25519 secret `UPDATE_SIGNING_KEY`.
- After a release, regenerate packaging and update the Homebrew tap, Scoop
  bucket and (once merged) the winget package. Full checklist:
  [docs/RELEASING.md](docs/RELEASING.md).

## Update signing key

- Public key: `updatePublicKey` in `desktop/update.go`.
- Private seed: GitHub Actions secret `UPDATE_SIGNING_KEY`; for local
  manifests `~/.config/git-airlock/update-signing-key` on the Holly WSL user.
- Rotating the key means `go run ./cmd/updatesign keygen`, replacing the secret
  **and** shipping a build with the new public key. Old clients cannot verify
  the new manifest, so rotation needs one release signed with the old key that
  ships the new key.
- Never commit the seed. `UPDATE_SIGNING_KEY` / `--key-file` are the only ways
  the signer reads it.

## Conventions and gotchas

- **Never force-push.** `gitutil.Sync` only fast-forwards and reports
  divergence.
- The worker validates requests; the API base and repository always come from
  the config, never from a queued request.
- Provider differences: Gitea only uses `(pull_request)` statuses; GitLab has
  no merge-after-rebase. Keep these special cases in mind when touching merge
  logic.
- The desktop pages are **server-rendered HTML**, not the generated Wails
  frontend. `window.go` bindings are unavailable there; use same-origin
  endpoints (`/_airlock/...`) as the repository picker does. `window.runtime`
  helpers are not available either — prefer HTTP and `WindowExecJS` from Go.
- The native menu navigates with `WindowExecJS` (`desktop/main.go`,
  `navigate`); keep URLs in sync with `ServeHTTP`.
- Queue reads use `git cat-file --batch`; do not reintroduce per-object
  `git show` calls (this was the SMB performance fix in 1.6.3).
- Windows MessageBox only offers Yes/No, which is why the update prompt is
  "Install now / Later" and has no "Skip".
- Wails logs a `Chromium.Focus` "parameter is incorrect" stack when a dialog is
  opened during startup; it is non-fatal and does not stop the app.
- Unsigned Windows binaries can be quarantined by Defender as
  `Trojan:Win32/Bearfoos.B!ml` (false positive). Up to 1.7.2 the task ran through
  `worker.vbs`, so the failure showed as a WSH dialog with `0x800700E1`, then
  `0x80070002` once the exe was quarantined. 1.7.3+ runs
  `airlock.exe worker --hidden` directly; failures are only in Task Scheduler
  history. Verify SHA256 and the Ed25519 signature before trusting a download;
  recovery steps and the SignPath signing plan are in
  [docs/CODE_SIGNING.md](docs/CODE_SIGNING.md).
- Windows builds carry PE metadata (`installer/winres.json` for the CLI, the
  `info` block in `desktop/wails.json` for the app) and are not stripped on
  purpose. Do not remove the metadata; SignPath requires it and it reduces
  Defender false positives.
- `installer/build.ps1` stamps `info.productVersion` from `-Version`; a bare
  `wails build` keeps the `0.0.0` placeholder.
- When replacing `airlock.exe` on Windows, stop the scheduled task first
  ("text file busy"). The task name is `Git Airlock`.

## Environment (the maintainer's setup)

- Windows dev box, WSL `Ubuntu20.04LTS`; repo at `Z:\repos\git-airlock`
  (SMB share from Holly) and `/mnt/user/Development/repos/git-airlock` in WSL.
- Go SDK: `C:\Users\eriki\bin\go-sdk\go`; `wails` and Go tools in
  `C:\Users\eriki\go\bin`.
- `gh` is only installed inside WSL; use
  `wsl.exe -d Ubuntu20.04LTS bash -lc 'gh ...'` from Windows.
- Desktop install: `%LOCALAPPDATA%\Programs\Git Airlock`; config in
  `%APPDATA%\git-airlock`; the worker runs as the interactive user task
  `Git Airlock` every minute.
- The CLI used by the agent/holly container is
  `/mnt/user/Development/repos/.bin/airlock` (update it from the release
  archive after each version).

## Current status (2026-10-29)

- Latest release: **v1.7.3** (unsigned stopgap: PE metadata, no VBS launcher).
  Homebrew tap and Scoop bucket are updated; the Linux `.bin/airlock` still runs
  1.7.1, which is fine because the CLI did not change.
- The Windows box still has a quarantined `airlock.exe` from the 1.7.2 update.
  Install 1.7.3 after adding the Defender exclusion (docs/CODE_SIGNING.md), then
  submit the 1.7.3 `airlock.exe` to Microsoft as a false positive. The scheduled
  task `Git Airlock` is defined and runs every minute.
- Code signing: application submitted to SignPath Foundation on 2026-10-29.
  Windows builds now carry PE metadata; after approval, enable the signing steps
  from docs/CODE_SIGNING.md.
- winget: the 1.6.0 package PR (#464029) is still in Microsoft review. The
  1.7.3 manifests are rendered; submit the update once it merges.
- Open roadmap items: SignPath code signing (pending approval), Linux deb/rpm
  packages, labels and review automation, optional in-app update banner.

## Testing checklist before a release

1. `go vet ./...` and `go test ./...` in the root module.
2. `cd desktop && go vet ./...` and a `wails build`.
3. `bash test/integration/run.sh` for queue/sync/merge changes.
4. Smoke-test the desktop: dashboard, repository detail, manual/logs pages,
   repository picker (Open dashboard, Add config…, remove, Cancel).
5. `airlock-desktop --check-updates` against the previous release: it must
   report the new version and verify the signature.
6. Only then tag and let the workflow build.
