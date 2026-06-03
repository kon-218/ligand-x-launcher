#!/bin/bash
# Build Ligand-X Launcher for current platform only.
#
# Usage:
#   build-current.sh           # default (dev) launcher -> build/bin/ligandx-launcher
#   build-current.sh public    # simplified public launcher -> build/bin/ligandx

set -e

cd "$(dirname "$0")/.."

VARIANT="${1:-dev}"

# Set PKG_CONFIG_PATH for Ubuntu 24.04 compatibility
export PKG_CONFIG_PATH="/usr/lib/x86_64-linux-gnu/pkgconfig:/usr/lib/pkgconfig:${PKG_CONFIG_PATH:-}"

# Locate wails CLI
WAILS_BIN="$(go env GOPATH)/bin/wails"
if [ ! -f "$WAILS_BIN" ]; then
    echo "Error: wails CLI not found"
    echo "Install with: go install github.com/wailsapp/wails/v2/cmd/wails@latest"
    exit 1
fi

if [ "$VARIANT" = "public" ]; then
    echo "Building PUBLIC launcher for current platform..."
    "$WAILS_BIN" build -tags public -o ligandx
else
    echo "Building for current platform..."
    "$WAILS_BIN" build
fi

echo ""
echo "Build complete!"
echo "Binary location: build/bin/"
ls -la build/bin/
