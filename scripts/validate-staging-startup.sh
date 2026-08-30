#!/usr/bin/env bash
# Start and validate the exact production Compose bundle in an isolated project.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

ENV_FILE="${ENV_FILE:-.env.production}"
OVERRIDE_ENV_FILE="${OVERRIDE_ENV_FILE:-}"
COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-ligandx-staging}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-900}"
POLL_SECONDS="${POLL_SECONDS:-10}"
CLEANUP="${CLEANUP:-true}"
LOG_DIR="${LOG_DIR:-}"

if [ ! -f "$ENV_FILE" ]; then
  echo "Missing env file: $ENV_FILE" >&2
  exit 1
fi
if [ -n "$OVERRIDE_ENV_FILE" ] && [ ! -f "$OVERRIDE_ENV_FILE" ]; then
  echo "Missing override env file: $OVERRIDE_ENV_FILE" >&2
  exit 1
fi

compose=(docker compose --project-name "$COMPOSE_PROJECT_NAME" --env-file "$ENV_FILE")
if [ -n "$OVERRIDE_ENV_FILE" ]; then
  compose+=(--env-file "$OVERRIDE_ENV_FILE")
fi

collect_diagnostics() {
  local status=$?
  if [ -n "$LOG_DIR" ]; then
    mkdir -p "$LOG_DIR"
    "${compose[@]}" ps -a --format json > "$LOG_DIR/compose-ps.jsonl" 2>/dev/null || true
    "${compose[@]}" logs --no-color > "$LOG_DIR/compose.log" 2>&1 || true
  fi
  if [ "$CLEANUP" = "true" ]; then
    "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
  fi
  exit "$status"
}
trap collect_diagnostics EXIT

VERSION_VALUE="$(awk -F= '$1=="VERSION"{gsub(/^[ \t]+|[ \t]+$/, "", $2); print $2}' "${OVERRIDE_ENV_FILE:-$ENV_FILE}" | tail -n 1)"
if [ -z "$VERSION_VALUE" ] || [ "$VERSION_VALUE" = "latest" ] || [[ "$VERSION_VALUE" == CHANGE_ME* ]]; then
  echo "VERSION must be pinned before staging validation." >&2
  exit 1
fi

mapfile -t expected_services < <("${compose[@]}" config --services | sort -u)
if [ "${#expected_services[@]}" -eq 0 ]; then
  echo "Compose configuration contains no services." >&2
  exit 1
fi
printf 'Starting %s services with VERSION=%s in project %s\n' \
  "${#expected_services[@]}" "$VERSION_VALUE" "$COMPOSE_PROJECT_NAME"
"${compose[@]}" pull
"${compose[@]}" up -d --remove-orphans

expected_csv="$(IFS=,; echo "${expected_services[*]}")"
export EXPECTED_SERVICES="$expected_csv"
deadline=$((SECONDS + TIMEOUT_SECONDS))
while [ "$SECONDS" -lt "$deadline" ]; do
  if "${compose[@]}" ps -a --format json | python3 -c '
import json, os, sys
expected = set(filter(None, os.environ["EXPECTED_SERVICES"].split(",")))
seen, bad = set(), []
for line in sys.stdin:
    if not line.strip():
        continue
    item = json.loads(line)
    name = item.get("Service")
    seen.add(name)
    state = (item.get("State") or "").lower()
    health = (item.get("Health") or "").lower()
    code = int(item.get("ExitCode") or 0)
    ready = (state == "running" and health in {"", "healthy"}) or (state == "exited" and code == 0)
    if not ready:
        bad.append((name, state, health, code))
missing = expected - seen
if missing or bad:
    print(f"waiting: ready={len(expected) - len(missing) - len(bad)}/{len(expected)} missing={sorted(missing)} bad={bad}")
    raise SystemExit(1)
print(f"all {len(expected)} configured services are ready")
'; then
    break
  fi
  sleep "$POLL_SECONDS"
done

"${compose[@]}" ps -a --format json | python3 -c '
import json, os, sys
expected = set(filter(None, os.environ["EXPECTED_SERVICES"].split(",")))
seen, bad = set(), []
for line in sys.stdin:
    if not line.strip():
        continue
    item = json.loads(line)
    name = item.get("Service")
    seen.add(name)
    state = (item.get("State") or "").lower()
    health = (item.get("Health") or "").lower()
    code = int(item.get("ExitCode") or 0)
    if not ((state == "running" and health in {"", "healthy"}) or (state == "exited" and code == 0)):
        bad.append((name, state, health, code))
missing = expected - seen
if missing or bad:
    print(f"Compose validation failed; missing={sorted(missing)} bad={bad}", file=sys.stderr)
    raise SystemExit(1)
'

GATEWAY_PORT_VALUE="$(awk -F= '$1=="GATEWAY_PORT"{print $2}' "${OVERRIDE_ENV_FILE:-$ENV_FILE}" | tail -n 1)"
FRONTEND_PORT_VALUE="$(awk -F= '$1=="FRONTEND_PORT"{print $2}' "${OVERRIDE_ENV_FILE:-$ENV_FILE}" | tail -n 1)"
export GATEWAY_PORT_VALUE="${GATEWAY_PORT_VALUE:-8000}"
export FRONTEND_PORT_VALUE="${FRONTEND_PORT_VALUE:-3000}"
python3 - <<'PY'
import os
import urllib.request

for name, url in (
    ("gateway", f"http://127.0.0.1:{os.environ['GATEWAY_PORT_VALUE']}/health"),
    ("frontend", f"http://127.0.0.1:{os.environ['FRONTEND_PORT_VALUE']}/"),
):
    with urllib.request.urlopen(url, timeout=15) as response:
        if response.status >= 400:
            raise SystemExit(f"{name} returned HTTP {response.status}")
        print(f"{name}: HTTP {response.status}")
PY

echo "Full production Compose validation passed."
