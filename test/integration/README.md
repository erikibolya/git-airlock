# Docker integration test

End-to-end test of the air-gap flow with a real Gitea:

1. Gitea starts with an empty repository.
2. The isolated side clones the bare mirror, pushes a branch and queues
   `pr open` (its config points its API at a dead address).
3. The worker syncs the mirror to Gitea, opens the PR and waits for checks.
4. A real Gitea Actions `act_runner` (host mode) runs
   `.gitea/workflows/ci.yml` for the `pull_request` event.
5. The worker merges with `merge_strategy = "squash"` and `delete_branch = true`.
6. The test asserts the workflow status is success, the PR is merged, the source
   branch is gone and `main` moved.

Run it with Docker (Compose v2):

```bash
bash test/integration/run.sh
```

The script tears down the compose project (including volumes) on exit. It needs
network access to pull `gitea/gitea:1.22.6` and `golang:1.25-alpine`.
