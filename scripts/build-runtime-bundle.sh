#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PUBLIC_REPO="${LIGANDX_PUBLIC_REPO:-$ROOT_DIR/../ligand-x}"
OUT_DIR="${1:-$ROOT_DIR/dist}"

if [ ! -x "$PUBLIC_REPO/scripts/build-runtime-bundle.sh" ]; then
  echo "ERROR: canonical public runtime builder not found at $PUBLIC_REPO/scripts/build-runtime-bundle.sh" >&2
  echo "       Set LIGANDX_PUBLIC_REPO to a pinned ligand-x checkout." >&2
  exit 66
fi
if [ ! -f "$PUBLIC_REPO/scripts/validate_runtime_bundle.py" ]; then
  echo "ERROR: canonical runtime validator not found in pinned public checkout: $PUBLIC_REPO" >&2
  exit 66
fi

exec env VERSION="${VERSION:-}" bash "$PUBLIC_REPO/scripts/build-runtime-bundle.sh" "$OUT_DIR"
