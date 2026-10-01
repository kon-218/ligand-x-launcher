#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PUBLIC_REPO="${LIGANDX_PUBLIC_REPO:-$ROOT_DIR/../ligand-x}"
RENDERER="$PUBLIC_REPO/scripts/render_stable_compose.py"
CANONICAL="$PUBLIC_REPO/docker-compose.yml"
TARGET="$ROOT_DIR/docker-compose.yml"
# The env template is the other half of the runtime bundle, and it drifted
# because only docker-compose.yml was ever synced here. Unlike the compose file
# it needs no rendering — but it cannot be copied wholesale either, since the
# launcher pins VERSION/PRO_VERSION to the release it ships with. Only the
# hardware-sizing keys are synced; see scripts/sync_env_template.py.
ENV_SYNC="$ROOT_DIR/scripts/sync_env_template.py"
CANONICAL_ENV="$PUBLIC_REPO/.env.production.template"
TARGET_ENV="$ROOT_DIR/.env.production.template"

for required in "$RENDERER" "$CANONICAL" "$ENV_SYNC" "$CANONICAL_ENV" "$TARGET_ENV"; do
  if [ ! -f "$required" ]; then
    echo "ERROR: required canonical topology source not found: $required" >&2
    exit 66
  fi
done

python3 "$RENDERER" "$CANONICAL" "$TARGET"
echo "Synchronized generated launcher topology: $TARGET"

python3 "$ENV_SYNC" "$CANONICAL_ENV" "$TARGET_ENV"

# Preview bundles are opt-in and must not replace the committed stable snapshot.
# PREVIEW_OUTPUT is required so a selected bundle cannot be written over
# docker-compose.yml by accident. Unknown labels fail inside the renderer.
if [ -n "${PREVIEW_BUNDLES:-}" ]; then
  if [ -z "${PREVIEW_OUTPUT:-}" ]; then
    echo "ERROR: PREVIEW_BUNDLES is set but PREVIEW_OUTPUT is empty. Refusing to overwrite the stable snapshot." >&2
    exit 1
  fi
  PREVIEW_ENV_OUTPUT="${PREVIEW_ENV_OUTPUT:-${PREVIEW_OUTPUT}.env}"
  python3 "$ROOT_DIR/scripts/render_preview_bundle.py" \
    --canonical "$CANONICAL" \
    --renderer "$RENDERER" \
    --bundles "$PREVIEW_BUNDLES" \
    --output "$PREVIEW_OUTPUT" \
    --env-template "$TARGET_ENV" \
    --env-output "$PREVIEW_ENV_OUTPUT"
fi
