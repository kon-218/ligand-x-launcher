#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PUBLIC_REPO="${LIGANDX_PUBLIC_REPO:-$ROOT_DIR/../ligand-x}"
RENDERER="$PUBLIC_REPO/scripts/render_stable_compose.py"
CANONICAL="$PUBLIC_REPO/docker-compose.yml"
SNAPSHOT="$ROOT_DIR/docker-compose.yml"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT
CANDIDATE="$TMP_DIR/docker-compose.yml"

for required in "$RENDERER" "$CANONICAL" "$SNAPSHOT" "$ROOT_DIR/.env.production.template"; do
  if [ ! -f "$required" ]; then
    echo "ERROR: required topology input not found: $required" >&2
    exit 66
  fi
done

python3 "$RENDERER" "$CANONICAL" "$CANDIDATE"
if ! cmp -s "$CANDIDATE" "$SNAPSHOT"; then
  echo "ERROR: launcher docker-compose.yml has drifted from canonical stable topology." >&2
  echo "       Run: make sync-runtime-topology" >&2
  diff -u "$SNAPSHOT" "$CANDIDATE" || true
  exit 1
fi

# The env template ships in the same runtime bundle and drifted from the
# canonical copy once already, which is what made the stack unstartable on a
# small machine. Checked here so CI catches it rather than a user.
python3 "$ROOT_DIR/scripts/sync_env_template.py" \
  "$PUBLIC_REPO/.env.production.template" "$ROOT_DIR/.env.production.template" --check

docker compose --env-file "$ROOT_DIR/.env.production.template" -f "$CANDIDATE" config --quiet

# Every declared CPU limit must be startable on the smallest supported machine.
# The runtime bundle auto-updates but the binary does not, so a user can be
# running the new template with an older launcher that has no fitting at all —
# the template's own values have to be valid unaided.
python3 - "$CANDIDATE" "$ROOT_DIR/.env.production.template" <<'PY'
import json, subprocess, sys

compose_file, env_file = sys.argv[1], sys.argv[2]
SMALLEST_SUPPORTED_CPUS = 4

model = json.loads(subprocess.run(
    ["docker", "compose", "--env-file", env_file, "-f", compose_file, "config", "--format", "json"],
    check=True, capture_output=True, text=True).stdout)

over = []
for name, svc in sorted(model.get("services", {}).items()):
    cpus = svc.get("deploy", {}).get("resources", {}).get("limits", {}).get("cpus")
    if cpus is not None and float(cpus) > SMALLEST_SUPPORTED_CPUS:
        over.append(f"{name} (cpus: {cpus})")

if over:
    print(
        f"ERROR: these services ask for more than {SMALLEST_SUPPORTED_CPUS} CPUs with the "
        f"shipped template, so a fresh install cannot start on a {SMALLEST_SUPPORTED_CPUS}-thread "
        f"machine:\n       " + ", ".join(over),
        file=sys.stderr,
    )
    sys.exit(1)
print(f"Resolved compose model starts on a {SMALLEST_SUPPORTED_CPUS}-CPU machine.")
PY

echo "Launcher topology snapshot matches canonical stable Compose semantics."
