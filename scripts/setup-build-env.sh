#!/bin/bash
# Setup build environment for Wails on Ubuntu 24.04

set -e

echo "Setting up Wails build environment..."

# Check if webkit2gtk-4.0.pc exists
if [ ! -f /usr/lib/x86_64-linux-gnu/pkgconfig/webkit2gtk-4.0.pc ]; then
    echo "Creating webkit2gtk-4.0.pc symlink..."
    
    # Check if webkit2gtk-4.1.pc exists
    if [ ! -f /usr/lib/x86_64-linux-gnu/pkgconfig/webkit2gtk-4.1.pc ]; then
        echo "ERROR: webkit2gtk-4.1.pc not found!"
        echo "Please install: sudo apt-get install libwebkit2gtk-4.1-dev"
        exit 1
    fi
    
    # Try to create symlink (will fail gracefully if no sudo)
    sudo ln -s /usr/lib/x86_64-linux-gnu/pkgconfig/webkit2gtk-4.1.pc \
               /usr/lib/x86_64-linux-gnu/pkgconfig/webkit2gtk-4.0.pc 2>/dev/null || {
        echo ""
        echo "WARNING: Could not create system symlink (requires sudo)."
        echo "Using PKG_CONFIG_PATH workaround instead..."
        echo ""
        echo "Add this to your ~/.bashrc or run before building:"
        echo ""
        echo 'export PKG_CONFIG_PATH="/usr/lib/x86_64-linux-gnu/pkgconfig:$PKG_CONFIG_PATH"'
        echo ""
        exit 0
    }
fi

echo "Build environment ready!"
