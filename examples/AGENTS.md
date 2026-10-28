## Git Airlock

This repository uses Git Airlock. `origin` is a local bare mirror, not the
private remote; a scheduled worker on a VPN-connected workstation forwards it.

- Push your work with `git push origin <branch>`. Never force-push `origin`.
- Open a pull request and merge it once CI is green:
  `airlock pr open --wait`
- Open without merging: `airlock pr open --merge never`
- Check an existing PR: `airlock pr status <number>`
- Merge an existing PR after checks: `airlock pr merge <number> --when-green`
- Validate without changing anything: add `--dry-run`

Exit codes from `airlock pr`: 0 = done, 1 = failed, 3 = still queued (the worker
retries; check the queue result later, do not resubmit).
