# Releasing Git Airlock

The release workflow (`.github/workflows/release.yml`) runs on a `v*` tag and
publishes the CLI archives, the Windows desktop build, the Inno Setup installer,
`checksums.txt` and the signed `latest.json` update manifest.

## 1. Prepare

```bash
# version without the tag prefix
VERSION=1.8.5
```

- Move the changelog entries under a new `## $VERSION — YYYY-MM-DD` heading in
  `CHANGELOG.md`.
- Add the release to the shipped list in `ROADMAP.md`.
- Bump the installer version:
  - `installer/git-airlock.iss`: `#define AppVersion "$VERSION"`
  - `installer/build.ps1`: `$Version = "$VERSION"`
- If the desktop changed, rebuild it locally and smoke-test (see the checklist
  in [AGENTS.md](../AGENTS.md)).

## 2. Verify locally

```bash
go vet ./... && go test ./...
cd desktop && go vet ./... && cd ..
```

```powershell
pwsh -File installer/build.ps1 -Config <a real config> -Version v$VERSION
```

Install that installer once and check the dashboard, the repository picker and
`airlock version`.

## 3. Tag and publish

```bash
git add -A
git commit -m "release: v$VERSION"
git push origin main
git tag -a v$VERSION -m "v$VERSION"
git push origin v$VERSION
```

Watch the run (the `gh` CLI is installed in WSL on the build host):

```bash
gh run list --limit 3
gh run watch <run-id>
```

The desktop job needs the `UPDATE_SIGNING_KEY` secret; it signs
`dist/latest.json` with the installer's SHA256. A missing or wrong secret fails
the job before the release is created.

If SignPath signing is enabled (see [CODE_SIGNING.md](CODE_SIGNING.md)), the
workflow stops at the signing requests until an approver releases them in the
SignPath UI: first the two executables, then the installer. `latest.json` is
generated from the signed installer, so approve both requests before the release
job runs.

## 4. Verify the release

```bash
gh release view v$VERSION --json assets -q '.assets[].name'
gh release download v$VERSION -p latest.json -O -
```

Expected assets: CLI archives for Linux/macOS/Windows, `airlock-desktop.exe`,
`GitAirlockSetup-$VERSION.exe`, `latest.json`, `checksums.txt`.

Check the manifest by running the previous installed version:

```powershell
& "$env:LOCALAPPDATA\Programs\Git Airlock\airlock-desktop.exe" --check-updates
```

It must print the new version, `update available`, and a SHA256 matching
`sha256sum GitAirlockSetup-$VERSION.exe`. The startup check and
**Help → Check for updates…** use the same code.

Once SignPath signing is enabled, verify the Authenticode signature too:

```powershell
Get-AuthenticodeSignature .\GitAirlockSetup-$VERSION.exe | Format-List
Get-AuthenticodeSignature "$env:LOCALAPPDATA\Programs\Git Airlock\airlock.exe" | Format-List
```

## 5. Update package managers

```bash
mkdir -p /tmp/airlock-$VERSION-assets
cd /tmp/airlock-$VERSION-assets
gh release download v$VERSION --repo erikibolya/git-airlock --clobber
cd /path/to/git-airlock
python3 packaging/render.py $VERSION /tmp/airlock-$VERSION-assets
```

`render.py` writes `packaging/out/git-airlock.rb`, `git-airlock.json` and the
winget manifests. Commit them only if you keep rendered output in tree (it is
gitignored by default).

### Homebrew

```bash
cp packaging/out/git-airlock.rb <tap>/Formula/git-airlock.rb
cd <tap> && git commit -am "git-airlock $VERSION" && git push
```

Tap: `erikibolya/homebrew-git-airlock`.

### Scoop

```bash
cp packaging/out/git-airlock.json <bucket>/bucket/git-airlock.json
cd <bucket> && git commit -am "git-airlock $VERSION" && git push
```

Bucket: `erikibolya/scoop-git-airlock`. It installs the CLI; the desktop app is
only in the Windows installer.

### winget

The initial package (`1.6.0`) was submitted in
[microsoft/winget-pkgs#464029](https://github.com/microsoft/winget-pkgs/pull/464029)
and waits for Microsoft review. Once merged, submit updates with:

```powershell
winget install Microsoft.WingetCreate
wingetcreate update ErikIbolya.GitAirlock -v $VERSION `
  -u "https://github.com/erikibolya/git-airlock/releases/download/v$VERSION/GitAirlockSetup-$VERSION.exe" `
  --submit
```

`winget validate --manifest packaging/out/winget` checks the rendered files
before submitting. The package identifier is `ErikIbolya.GitAirlock`; keep the
publisher and license fields in sync with `packaging/winget`.

## 6. Internal binaries

Update the CLI used by the agent and containers from the release archive:

```bash
install -m 0755 airlock-linux-amd64 /path/to/repos/.bin/airlock
/path/to/repos/.bin/airlock version
```

If a worker or container runs its own copy, stop it first (on Windows the
scheduled task is `Git Airlock`), replace the binary and start it again.

## Dev builds

A push to `dev` that touches `desktop/`, `cmd/`, `internal/` or `installer/`
(or a manual **Dev build** workflow run) publishes a rolling `dev` prerelease:

- `GitAirlockSetup-<base>-dev.<run>.exe` — Windows installer;
- `dev.json` — the signed manifest the desktop app reads from
  **Help → Install latest dev build…**;
- `airlock-desktop.exe` and `checksums.txt`.

The version is the next patch above the newest tag (`v1.8.6` →
`1.8.7-dev.<run>`). The `dev` release is marked as a prerelease, so
`releases/latest` and the normal update check still point at the stable
release. **Help → Revert to last stable version…** installs `latest.json`
again, even when its version is lower than the running dev build.

The first dev build has to be installed manually (download the installer from
the `dev` release); after that the menu items handle both directions.

## Signing key rotation

1. `go run ./cmd/updatesign keygen --private-key-file <path>`
2. `gh secret set UPDATE_SIGNING_KEY --repo erikibolya/git-airlock < <path>`
3. Put the new public key into `updatePublicKey` in `desktop/update.go`.
4. Ship one release **signed with the old key** that contains the new public
   key; only then switch the secret.

Clients verify `version + "\n" + sha256` with the embedded Ed25519 public key,
so a manifest signed by an unknown key is rejected.

## Known distributions caveats

- Windows binaries are not Authenticode-signed until SignPath approves the
  project (see [CODE_SIGNING.md](CODE_SIGNING.md)), so SmartScreen may warn and
  Defender occasionally quarantines recent builds as
  `Trojan:Win32/Bearfoos.B!ml` (false positive). Verify the published SHA256 and
  the update signature, then follow the recovery steps in CODE_SIGNING.md.
- The updater only runs on Windows. Homebrew, Scoop and winget users upgrade
  through their package manager; macOS/Linux builds of the desktop app show the
  release page instead.
