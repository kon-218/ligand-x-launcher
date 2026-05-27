#!/bin/bash
# Build Ligand-X Launcher for current platform only

set -e

cd "$(dirname "$0")/.."

# Set PKG_CONFIG_PATH for Ubuntu 24.04 compatibility
export PKG_CONFIG_PATH="/usr/lib/x86_64-linux-gnu/pkgconfig:/usr/lib/pkgconfig:${PKG_CONFIG_PATH:-}"

# Locate wails CLI
WAILS_BIN="$(go env GOPATH)/bin/wails"
if [ ! -f "$WAILS_BIN" ]; then
    echo "Error: wails CLI not found"
    echo "Install with: go install github.com/wailsapp/wails/v2/cmd/wails@latest"
    exit 1
fi

echo "Building for current platform..."
"$WAILS_BIN" build

echo ""
echo "Build complete!"
echo "Binary location: build/bin/"
ls -la build/bin/
