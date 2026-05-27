#!/bin/bash
# Build script with proper environment setup for Ubuntu 24.04

set -e

cd "$(dirname "$0")/.."

# Set PKG_CONFIG_PATH to help find webkit libraries
export PKG_CONFIG_PATH="/usr/lib/x86_64-linux-gnu/pkgconfig:/usr/lib/pkgconfig:${PKG_CONFIG_PATH:-}"

echo "Building Ligand-X Launcher (Development Mode)..."
echo "Using PKG_CONFIG_PATH: $PKG_CONFIG_PATH"
echo ""

# Run wails dev with the proper environment
WAILS_BIN="$(go env GOPATH)/bin/wails"

if [ ! -f "$WAILS_BIN" ]; then
    echo "Error: wails CLI not found at $WAILS_BIN"
    echo "Install with: go install github.com/wailsapp/wails/v2/cmd/wails@latest"
    exit 1
fi

"$WAILS_BIN" dev
