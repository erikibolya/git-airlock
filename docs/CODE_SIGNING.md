# Code signing

Git Airlock ships Windows executables (`airlock.exe`, `airlock-desktop.exe`,
`GitAirlockSetup-<version>.exe`). Unsigned binaries are periodically quarantined
by Microsoft Defender's ML heuristics as `Trojan:Win32/Bearfoos.B!ml`, which stops
the scheduled worker (`worker.vbs` fails with `0x800700E1`, then `0x80070002` once
the file is gone). Signing is the only reliable fix; there is no allow-list for
unsigned software.

## Policy

Free code signing provided by [SignPath.io](https://signpath.io/), certificate by
[SignPath Foundation](https://signpath.org/).

**Status:** application submitted on 2026-10-29. Releases remain unsigned until
SignPath approves the project and the workflow steps below are enabled.

The user-facing copy of this policy, including the team roles and the privacy
statement, lives in [README.md](../README.md#code-signing-policy). SignPath
requires the term "Code signing policy" on the project home page and on the
release pages, so keep that section in the README and the release footer in
`.github/workflows/release.yml`.

Signing rules:

- Only artifacts built by [`.github/workflows/release.yml`](../.github/workflows/release.yml)
  on GitHub-hosted runners are submitted.
- The signing approver reviews the run and the diff since the previous tag before
  approving the SignPath request.
- `airlock.exe` and `airlock-desktop.exe` are signed **before** the installer is
  built, so the copies inside the installer are signed too. The installer is then
  signed as well.
- The Ed25519 signature of `latest.json` (see
  [RELEASING.md](RELEASING.md#update-signing-key)) is independent of Authenticode
  and stays in place.

## Applying

1. Apply at <https://signpath.org/apply> with the repository URL
   (`https://github.com/erikibolya/git-airlock`), the release page URL and a
   one-paragraph description. Mention that the project is MIT, actively released
   and that the Windows binaries are distributed from GitHub Releases.
2. After approval, SignPath creates the project (suggested slug `git-airlock`)
   and an Open Source Code Signing policy (suggested `release-signing`).
3. In SignPath, add **GitHub** as a trusted build system for the repository. The
   OSS policy requires all artifacts to come from GitHub-hosted runners in this
   workflow.
4. Create two artifact configurations, using **Upload an artifact sample** with a
   real release:
   - `exes` — a ZIP containing `airlock.exe` and `airlock-desktop.exe`.
   - `installer` — `GitAirlockSetup-<version>.exe`.
5. Add the repository variables and secret used by the workflow:

   | Name | Type | Example |
   | --- | --- | --- |
   | `SIGNPATH_ORGANIZATION_ID` | variable | `00000000-0000-0000-0000-000000000000` |
   | `SIGNPATH_PROJECT_SLUG` | variable | `git-airlock` |
   | `SIGNPATH_POLICY_SLUG` | variable | `release-signing` |
   | `SIGNPATH_EXES_CONFIG` | variable | `exes` |
   | `SIGNPATH_INSTALLER_CONFIG` | variable | `installer` |
   | `SIGNPATH_API_TOKEN` | **secret** | token of a SignPath user with submitter permission |

6. Enable the signing steps (next section) and test with the next release.

SignPath manually approves every release. When the workflow waits for
completion, approve the request in the SignPath UI; `wait-for-completion: true`
then downloads the signed artifact.

## Release build order

The installer embeds the executables, so the order matters:

1. `wails build` → `desktop/build/bin/airlock-desktop.exe`
2. `installer/build.ps1 -SkipDesktop -SkipInstaller -Version <v>` → builds
   `installer/files/airlock.exe` (with PE metadata) and copies the desktop exe.
3. Sign both executables (SignPath request 1, artifact config `exes`).
4. Put the signed files back into `desktop/build/bin/` and `installer/files/`.
5. `installer/build.ps1 -SkipDesktop -SkipCLI -Version <v>` → builds the
   installer from the signed payload.
6. Sign the installer (SignPath request 2, artifact config `installer`).
7. Generate `latest.json` **after** step 6, because it contains the installer's
   SHA256, and sign it with the Ed25519 update key.
8. Upload the signed installer, the signed executables and `latest.json`.

`-SkipCLI` exists so step 5 cannot overwrite the signed `airlock.exe`.

## Workflow patch

Once the SignPath values exist, add these steps to the `desktop` job in
`.github/workflows/release.yml`. They are intentionally not enabled yet: the
action needs the organization/project/policy slugs from the approved SignPath
project.

```yaml
      - name: Stage unsigned executables
        shell: pwsh
        run: |
          New-Item -ItemType Directory -Force sign-input | Out-Null
          Copy-Item desktop/build/bin/airlock-desktop.exe sign-input/
          Copy-Item installer/files/airlock.exe sign-input/

      - name: Upload unsigned executables
        id: unsigned-exes
        uses: actions/upload-artifact@v4
        with:
          name: unsigned-exes
          path: sign-input/*

      - name: Sign executables
        uses: signpath/github-action-submit-signing-request@v3
        with:
          api-token: ${{ secrets.SIGNPATH_API_TOKEN }}
          organization-id: ${{ vars.SIGNPATH_ORGANIZATION_ID }}
          project-slug: ${{ vars.SIGNPATH_PROJECT_SLUG }}
          signing-policy-slug: ${{ vars.SIGNPATH_POLICY_SLUG }}
          artifact-configuration-slug: ${{ vars.SIGNPATH_EXES_CONFIG }}
          github-artifact-id: ${{ steps.unsigned-exes.outputs.artifact-id }}
          wait-for-completion: true
          output-artifact-directory: signed-exes

      - name: Use signed executables
        shell: pwsh
        run: |
          Copy-Item signed-exes/airlock-desktop.exe desktop/build/bin/ -Force
          Copy-Item signed-exes/airlock.exe installer/files/ -Force
```

Repeat the same upload/submit/download pattern for
`installer/dist/GitAirlockSetup-*.exe` with `SIGNPATH_INSTALLER_CONFIG`, then
build `latest.json` from the signed file.

## Metadata requirements

SignPath requires every signed binary to carry a product name and the same
product version as the release:

- `airlock.exe` — [installer/winres.json](../installer/winres.json) is rendered by
  [`go-winres`](https://github.com/tc-hib/go-winres) during the build. It sets
  `CompanyName`, `ProductName`, `FileDescription`, `FileVersion`,
  `ProductVersion`, the icon and a manifest.
- `airlock-desktop.exe` — the `info` block in
  [desktop/wails.json](../desktop/wails.json) feeds Wails' Windows resource.
  `installer/build.ps1` and the release workflow stamp `productVersion` from the
  tag before building.

Windows builds keep their symbols (no `-ldflags "-s -w"`). Stripped, anonymous Go
binaries score worse with Defender's ML model; the size cost is a few megabytes.

## Defender false positives

Symptom: `worker.vbs` reports line 5 with `0x800700E1` ("the file contains a virus
or potentially unwanted software"), then `0x80070002` after Defender quarantines
the executable. The worker stops running and the dialog can reappear every
minute.

1. Confirm what was flagged (PowerShell):

   ```powershell
   Get-MpThreatDetection | Sort-Object InitialDetectionTime -Descending |
     Select-Object -First 5 ThreatName, Resources | Format-List
   Get-ChildItem "$env:LOCALAPPDATA\Programs\Git Airlock" | Select-Object Name, Length
   Get-Content "$env:LOCALAPPDATA\git-airlock\worker.vbs"
   ```

   The script must match the generated template (`rc = sh.Run("...\airlock.exe"
   worker ...)` on line 5). Anything else needs investigation.

2. Verify the release before trusting it: compare
   `Get-FileHash .\GitAirlockSetup-<version>.exe -Algorithm SHA256` with
   `checksums.txt`, and run `airlock-desktop --check-updates` to verify the
   Ed25519 signature of `latest.json`.

3. Restore and allow: Windows Security → Virus & threat protection → Protection
   history → **Allow on device**, or as administrator:

   ```powershell
   Add-MpPreference -ExclusionPath "$env:LOCALAPPDATA\Programs\Git Airlock"
   & "$env:ProgramFiles\Windows Defender\MpCmdRun.exe" -Restore -Name "<ThreatName>"
   ```

4. If the file is gone, reinstall the verified installer and run the task:

   ```powershell
   schtasks /change /tn "Git Airlock" /enable
   schtasks /run /tn "Git Airlock"
   ```

5. Report the false positive at <https://www.microsoft.com/en-us/wdsi/filesubmission>
   ("Should not be detected"), attaching the quarantined `airlock.exe`. This fixes
   that specific hash; a new release has a new hash, which is why signing is the
   permanent fix.

## Verifying a signed release

```powershell
signtool verify /pa /v GitAirlockSetup-<version>.exe
Get-AuthenticodeSignature .\GitAirlockSetup-<version>.exe | Format-List
```

```bash
airlock-desktop --check-updates
sha256sum -c checksums.txt
```

The certificate chain must lead to the SignPath Foundation and the file version
must match the release tag.
