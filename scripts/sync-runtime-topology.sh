#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PUBLIC_REPO="${LIGANDX_PUBLIC_REPO:-$ROOT_DIR/../ligand-x}"
RENDERER="$PUBLIC_REPO/scripts/render_stable_compose.py"
CANONICAL="$PUBLIC_REPO/docker-compose.yml"
TARGET="$ROOT_DIR/docker-compose.yml"

for required in "$RENDERER" "$CANONICAL"; do
  if [ ! -f "$required" ]; then
    echo "ERROR: required canonical topology source not found: $required" >&2
    exit 66
  fi
done

python3 "$RENDERER" "$CANONICAL" "$TARGET"
echo "Synchronized generated launcher topology: $TARGET"
