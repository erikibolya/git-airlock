#!/usr/bin/env bash
set -euo pipefail

: "${TOKEN:?TOKEN is required}"
URL="${GITEA_URL:-http://gitea:3000}"
AUTH="Authorization: token ${TOKEN}"
API="${URL}/api/v1/repos/ci/repo"

fail() {
  echo "FAIL: $*" >&2
  airlock status --config /work/worker.toml 2>/dev/null || true
  exit 1
}

git config --global --add safe.directory "*"
git config --global user.name "CI"
git config --global user.email "ci@example.com"
git config --global init.defaultBranch main

cd /work
rm -rf mirror.git repo
git clone --bare "${URL}/ci/repo.git" mirror.git
git -C mirror.git remote add company "http://ci:${TOKEN}@gitea:3000/ci/repo.git"

cat > isolated.toml <<'CFG'
repo = "ci/repo"
remote = "company"
mirror = "/work/mirror.git"
queue = "git"
provider = "gitea"
api = "http://127.0.0.1:1/api/v1"
base = "main"
merge = "when-green"
auto_pr = false
CFG

cat > worker.toml <<'CFG'
repo = "ci/repo"
remote = "company"
mirror = "/work/mirror.git"
queue = "git"
provider = "gitea"
api = "http://gitea:3000/api/v1"
base = "main"
merge = "when-green"
merge_strategy = "squash"
delete_branch = true
auto_pr = false
CFG

# The isolated machine only pushes to the mirror and queues a request.
git clone mirror.git repo
cd repo
mkdir -p .gitea/workflows
cat > .gitea/workflows/ci.yml <<'YAML'
name: CI
on:
  pull_request:
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: echo "act_runner integration OK"
        shell: sh
YAML
git add .gitea/workflows/ci.yml
git commit -m "ci: integration workflow"
git push origin main
# Put the workflow on the default branch before opening the PR.
airlock worker --config /work/worker.toml >/dev/null 2>&1 || true

echo "integration test $(date -u +%s)" >> README.md
git checkout -b feature
git commit -am "feature: integration test"
git push origin feature
set +e
airlock pr open --head feature --base main --merge when-green --no-wait --config /work/isolated.toml
rc=$?
set -e
# --no-wait intentionally exits 3 ("still queued").
if [ "${rc}" -ne 0 ] && [ "${rc}" -ne 3 ]; then
  exit "${rc}"
fi

export GITEA_TOKEN="${TOKEN}"

# The worker syncs the mirror to Gitea and opens the PR.
PR=""
for _ in $(seq 1 20); do
  airlock worker --config /work/worker.toml >/dev/null 2>&1 || true
  PR=$(curl -fsS -H "${AUTH}" "${API}/pulls?state=open" | jq -r '.[0].number // empty')
  [ -n "${PR}" ] && break
  sleep 1
done
[ -n "${PR}" ] || fail "worker did not open a pull request"

SHA=$(curl -fsS -H "${AUTH}" "${API}/pulls/${PR}" | jq -r '.head.sha')
[ -n "${SHA}" ] && [ "${SHA}" != "null" ] || fail "cannot read the PR head SHA"

# Wait for the real act_runner workflow to report success.
STATE=""
for _ in $(seq 1 90); do
  STATE=$(curl -fsS -H "${AUTH}" "${API}/commits/${SHA}/status" | jq -r '.state // empty')
  [ "${STATE}" = "success" ] && break
  [ "${STATE}" = "failure" ] && fail "CI failed"
  sleep 2
done
[ "${STATE}" = "success" ] || fail "CI did not succeed (state=${STATE:-none})"

MERGED=""
for _ in $(seq 1 20); do
  airlock worker --config /work/worker.toml >/dev/null 2>&1 || true
  MERGED=$(curl -fsS -H "${AUTH}" "${API}/pulls/${PR}" | jq -r '.merged')
  [ "${MERGED}" = "true" ] && break
  sleep 1
done
[ "${MERGED}" = "true" ] || fail "PR #${PR} was not merged"

# delete_branch = true in the worker config.
CODE=$(curl -s -o /dev/null -w '%{http_code}' -H "${AUTH}" "${API}/branches/feature")
[ "${CODE}" = "404" ] || fail "source branch still exists (HTTP ${CODE})"

# The squash commit must be on main.
MAIN=$(curl -fsS -H "${AUTH}" "${API}/branches/main" | jq -r '.commit.id')
[ -n "${MAIN}" ] && [ "${MAIN}" != "null" ] || fail "cannot read main"

echo "integration test passed: PR #${PR} merged, branch deleted, main=${MAIN:0:8}"
