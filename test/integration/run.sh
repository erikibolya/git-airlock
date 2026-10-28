#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

COMPOSE="docker compose -f docker-compose.yml"
URL="http://127.0.0.1:3000"

$COMPOSE down -v --remove-orphans >/dev/null 2>&1 || true
rm -rf workspace
mkdir -p workspace
cleanup() { $COMPOSE down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

$COMPOSE build tool
$COMPOSE up -d gitea
for _ in $(seq 1 60); do
  curl -fsS --max-time 3 "${URL}/api/v1/version" >/dev/null 2>&1 && break
  sleep 2
done
curl -fsS --max-time 3 "${URL}/api/v1/version" >/dev/null || { echo "gitea did not start"; exit 1; }

# Gitea refuses to run its CLI as root; the image's service user is "git".
$COMPOSE exec -T -u git gitea gitea admin user create \
  --username ci --password 'ci-pass-1234' --email ci@example.com \
  --admin --must-change-password=false >/dev/null 2>&1 || true

TOKEN=$($COMPOSE exec -T -u git gitea gitea admin user generate-access-token \
  --username ci --token-name integration --scopes all | awk '/Access token/ {print $NF}' | tail -n1)
if [ -z "${TOKEN}" ]; then
  echo "could not create a Gitea token" >&2
  exit 1
fi

curl -fsS -H "Authorization: token ${TOKEN}" -H "Content-Type: application/json" \
  -d '{"name":"repo","auto_init":true,"default_branch":"main","private":false}' \
  "${URL}/api/v1/user/repos" >/dev/null

# Start the real Gitea Actions runner. Job containers are scheduled on the dind
# service (see runner-config.yaml), so the test covers container jobs too.
RUNNER_TOKEN=$(curl -fsS -H "Authorization: token ${TOKEN}" \
  "${URL}/api/v1/admin/runners/registration-token" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
if [ -z "${RUNNER_TOKEN}" ]; then
  echo "could not create a runner registration token" >&2
  exit 1
fi
RUNNER_TOKEN="${RUNNER_TOKEN}" $COMPOSE up -d runner
for _ in $(seq 1 30); do
  $COMPOSE logs runner 2>&1 | grep -qi "registered" && break
  sleep 1
done
if ! $COMPOSE logs runner 2>&1 | grep -qi "registered"; then
  echo "act_runner did not register:" >&2
  $COMPOSE logs runner 2>&1 | tail -20 >&2
  exit 1
fi

$COMPOSE run --rm -T -e TOKEN="${TOKEN}" tool bash /run/airlock/test.sh
