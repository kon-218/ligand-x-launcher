#!/usr/bin/env bash
# Validate that a pinned production release starts cleanly in staging.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

ENV_FILE="${ENV_FILE:-.env.production}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-420}"
POLL_SECONDS="${POLL_SECONDS:-10}"
EXPECTED_TOTAL="${EXPECTED_TOTAL:-25}"
CRITICAL_SERVICES=(
  docking
  md
  admet
  reinvent
  rbfe
  kinetics
  abfe
  worker-cpu
  worker-qc
)

if [ ! -f "$ENV_FILE" ]; then
  echo "Missing env file: $ENV_FILE" >&2
  exit 1
fi

VERSION_VALUE="$(awk -F= '$1=="VERSION"{gsub(/^[ \t]+|[ \t]+$/, "", $2); print $2}' "$ENV_FILE" | tail -n 1)"
if [ -z "$VERSION_VALUE" ] || [ "$VERSION_VALUE" = "latest" ] || [[ "$VERSION_VALUE" == CHANGE_ME* ]]; then
  echo "VERSION must be pinned in $ENV_FILE before staging validation." >&2
  exit 1
fi

echo "Starting staging stack with VERSION=$VERSION_VALUE"
docker compose --env-file "$ENV_FILE" up -d

load_compose_ps_json() {
  docker compose --env-file "$ENV_FILE" ps --format json
}

deadline=$((SECONDS + TIMEOUT_SECONDS))
while [ $SECONDS -lt $deadline ]; do
  healthy_count="$(load_compose_ps_json | python3 -c '
import json
import sys

ok = {"running", "healthy"}
count = 0
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    item = json.loads(line)
    state = (item.get("Health") or item.get("State") or "").lower()
    if state in ok:
        count += 1
print(count)
')"

  echo "Healthy/running: ${healthy_count}/${EXPECTED_TOTAL}"
  if [ "$healthy_count" -ge "$EXPECTED_TOTAL" ]; then
    break
  fi
  sleep "$POLL_SECONDS"
done

CRITICAL_CSV="$(IFS=,; echo "${CRITICAL_SERVICES[*]}")"
export EXPECTED_TOTAL CRITICAL_CSV
load_compose_ps_json | python3 -c '
import json
import os
import sys

expected_total = int(os.environ["EXPECTED_TOTAL"])
critical = [name for name in os.environ["CRITICAL_CSV"].split(",") if name]
ok = {"running", "healthy"}

services = {}
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    item = json.loads(line)
    services[item["Service"]] = item

healthy = 0
bad = []
for name, item in sorted(services.items()):
    state = (item.get("Health") or item.get("State") or "").lower()
    if state in ok:
        healthy += 1
    else:
        bad.append((name, state or "unknown"))

if healthy < expected_total:
    print(
        f"Need at least {expected_total} healthy/running services, saw {healthy} "
        f"(of {len(services)} defined).",
        file=sys.stderr,
    )
    if bad:
        print("Unhealthy services:", file=sys.stderr)
        for name, state in bad:
            print(f"  - {name}: {state}", file=sys.stderr)
    sys.exit(1)

for name in critical:
    state = (services.get(name, {}).get("Health") or services.get(name, {}).get("State") or "").lower()
    if state not in ok:
        print(f"Critical service {name} is not healthy/running (state={state or 'missing'}).", file=sys.stderr)
        sys.exit(1)

if bad:
    print("Note: some non-critical services are not healthy/running (stack still passed):")
    for name, state in bad:
        print(f"  - {name}: {state}")

print(f"Validation passed: {healthy}/{expected_total} services healthy/running (minimum).")
'
