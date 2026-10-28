# Packaging

Rendered manifests live in `packaging/out/` (gitignored). Regenerate them for a
release after the GitHub release assets exist:

```bash
python3 packaging/render.py 1.6.0 /path/to/release/assets
```

Missing assets are downloaded from
`https://github.com/erikibolya/git-airlock/releases/download/v<version>/`.

## Homebrew

The formula is published in the tap
[`erikibolya/homebrew-git-airlock`](https://github.com/erikibolya/homebrew-git-airlock):

```bash
brew tap erikibolya/git-airlock
brew install git-airlock
```

Copy `packaging/out/git-airlock.rb` to `Formula/git-airlock.rb` in the tap and
push.

## Scoop

The manifest is published in the bucket
[`erikibolya/scoop-git-airlock`](https://github.com/erikibolya/scoop-git-airlock):

```powershell
scoop bucket add git-airlock https://github.com/erikibolya/scoop-git-airlock
scoop install git-airlock/git-airlock
```

Copy `packaging/out/git-airlock.json` to `bucket/git-airlock.json` in the bucket
and push. The Scoop package installs the CLI; use the Windows installer for the
desktop app.

## winget

The three manifests in `packaging/out/winget/` follow the
[winget-pkgs](https://github.com/microsoft/winget-pkgs) layout:

```
manifests/e/ErikIbolya/GitAirlock/<version>/
  ErikIbolya.GitAirlock.yaml
  ErikIbolya.GitAirlock.installer.yaml
  ErikIbolya.GitAirlock.locale.en-US.yaml
```

Validate locally with `winget validate --manifest <dir>` and submit with
[`wingetcreate`](https://github.com/microsoft/winget-create):

```powershell
winget install Microsoft.WingetCreate
wingetcreate submit --manifest <dir>
```

The installer is Inno Setup, per-user, and keeps an existing `.airlock.toml` on
upgrade. The package identifier is `ErikIbolya.GitAirlock`.
