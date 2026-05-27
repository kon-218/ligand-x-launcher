#!/bin/bash
# Build Ligand-X Launcher for all platforms
# This script builds native binaries. For full packaging (AppImage, DMG, NSIS),
# use the GitHub Actions CI/CD workflow instead.

set -e

VERSION="${1:-1.0.0}"
OUTPUT_DIR="dist"

echo "Building Ligand-X Launcher v${VERSION}"
echo "======================================="
echo ""
echo "Note: This script builds raw binaries only."
echo "For proper installers (AppImage/DMG/NSIS), push a tag to trigger CI:"
echo "  git tag launcher-v${VERSION} && git push origin launcher-v${VERSION}"
echo ""

cd "$(dirname "$0")/.."

# Set PKG_CONFIG_PATH for Ubuntu 24.04 compatibility
export PKG_CONFIG_PATH="/usr/lib/x86_64-linux-gnu/pkgconfig:/usr/lib/pkgconfig:${PKG_CONFIG_PATH:-}"

# Create output directory
mkdir -p "$OUTPUT_DIR"

# Locate wails CLI
WAILS_BIN="$(go env GOPATH)/bin/wails"
if [ ! -f "$WAILS_BIN" ]; then
    echo "Error: wails CLI not found"
    echo "Install with: go install github.com/wailsapp/wails/v2/cmd/wails@latest"
    exit 1
fi

# Detect current platform
CURRENT_OS=$(uname -s | tr '[:upper:]' '[:lower:]')
CURRENT_ARCH=$(uname -m)

case "$CURRENT_ARCH" in
    x86_64) CURRENT_ARCH="amd64" ;;
    aarch64|arm64) CURRENT_ARCH="arm64" ;;
esac

echo "Detected platform: ${CURRENT_OS}/${CURRENT_ARCH}"
echo ""

# Build for Linux (amd64)
if [ "$CURRENT_OS" = "linux" ]; then
    echo "Building for Linux (amd64)..."
    "$WAILS_BIN" build -platform linux/amd64 -o ligandx-launcher-linux-amd64
    mv build/bin/ligandx-launcher-linux-amd64 "$OUTPUT_DIR/"
    echo "✓ Linux build complete"
fi

# Build for Windows (amd64) - cross-compile from Linux/macOS
echo ""
echo "Building for Windows (amd64)..."
"$WAILS_BIN" build -platform windows/amd64 -o ligandx-launcher.exe 2>/dev/null && {
    mv build/bin/ligandx-launcher.exe "$OUTPUT_DIR/ligandx-launcher-windows-amd64.exe"
    echo "✓ Windows build complete"
} || {
    echo "⚠ Windows cross-compilation skipped (may require additional setup)"
}

# Build for macOS (amd64) - Intel
if [ "$CURRENT_OS" = "darwin" ]; then
    echo ""
    echo "Building for macOS (amd64)..."
    "$WAILS_BIN" build -platform darwin/amd64 -o ligandx-launcher-darwin-amd64
    if [ -d "build/bin/ligandx-launcher-darwin-amd64.app" ]; then
        cp -r "build/bin/ligandx-launcher-darwin-amd64.app" "$OUTPUT_DIR/"
    else
        mv build/bin/ligandx-launcher-darwin-amd64 "$OUTPUT_DIR/" 2>/dev/null || true
    fi
    echo "✓ macOS Intel build complete"

    # Build for macOS (arm64) - Apple Silicon
    echo ""
    echo "Building for macOS (arm64)..."
    "$WAILS_BIN" build -platform darwin/arm64 -o ligandx-launcher-darwin-arm64
    if [ -d "build/bin/ligandx-launcher-darwin-arm64.app" ]; then
        cp -r "build/bin/ligandx-launcher-darwin-arm64.app" "$OUTPUT_DIR/"
    else
        mv build/bin/ligandx-launcher-darwin-arm64 "$OUTPUT_DIR/" 2>/dev/null || true
    fi
    echo "✓ macOS ARM build complete"
fi

echo ""
echo "======================================="
echo "Build complete! Binaries in $OUTPUT_DIR/"
ls -la "$OUTPUT_DIR/"
echo ""
echo "For proper installers, use GitHub Actions:"
echo "  git tag launcher-v${VERSION}"
echo "  git push origin launcher-v${VERSION}"
