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

docker compose --env-file "$ROOT_DIR/.env.production.template" -f "$CANDIDATE" config --quiet
echo "Launcher topology snapshot matches canonical stable Compose semantics."
