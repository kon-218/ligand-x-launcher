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

# Config files bind-mounted by docker-compose.yml. These must exist on the host
# or Docker auto-creates the missing source as a directory and fails to mount it
# onto a file ("not a directory"). Keep this list in sync with runtimeEntryAllowed
# in app.go and the relative bind mounts in docker-compose.yml.
#
# Guard against the recurring failure mode: running `docker compose up` from this
# repo with a missing source file makes Docker vivify it as an empty *directory*
# in place. `cp` (no -r) would then either error or copy nothing, silently
# shipping a bundle that re-triggers the mount bug on every user. Fail loudly if
# any bind-mount source is not a regular file so the bundle is never built broken.
BIND_CONFIGS="docker/nginx/ligandx.conf config/rabbitmq.conf config/flower_config.py"
for f in $BIND_CONFIGS; do
  if [ ! -f "$f" ]; then
    echo "ERROR: bind-mount config source '$f' is not a regular file." >&2
    if [ -d "$f" ]; then
      echo "       It is a directory (Docker likely auto-created it during a local 'compose up')." >&2
      echo "       Restore the real file, e.g.: rmdir '$f' && git checkout -- '$f'" >&2
    fi
    exit 1
  fi
done
mkdir -p "$STAGE/docker/nginx" "$STAGE/config"
cp docker/nginx/ligandx.conf "$STAGE/docker/nginx/"
cp config/rabbitmq.conf config/flower_config.py "$STAGE/config/"

(
  cd "$STAGE"
  zip -q -r "$BUNDLE" . -x "**/.DS_Store"
)

echo "$BUNDLE"
