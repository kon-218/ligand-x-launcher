#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${1:-$ROOT_DIR/dist}"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"
BUNDLE="$OUT_DIR/ligand-x-runtime.zip"
rm -f "$BUNDLE"

cd "$ROOT_DIR"
cp docker-compose.yml .env.production.template README.md LICENSE "$STAGE/"
mkdir -p "$STAGE/data/license" "$STAGE/opt/deeppocket_models"

(
  cd "$STAGE"
  zip -q -r "$BUNDLE" . -x "**/.DS_Store"
)

echo "$BUNDLE"
