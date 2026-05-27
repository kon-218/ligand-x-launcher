# Building the Launcher

## Prerequisites

1. **Go 1.21+**
2. **Wails CLI** - Install with:
   ```bash
   go install github.com/wailsapp/wails/v2/cmd/wails@latest
   ```

## Platform-Specific Setup

### Linux
Install development libraries:
```bash
sudo apt-get install libgtk-3-dev libwebkit2gtk-4.0-dev pkg-config
```

### Windows
- No additional setup needed (uses WebView2)

### macOS
- Xcode Command Line Tools required:
  ```bash
  xcode-select --install
  ```

## Building

### Current Platform Only
```bash
cd launcher
./scripts/build-current.sh
```

Binary will be in `build/bin/`

### All Platforms (Cross-compilation)
```bash
cd launcher
./scripts/build-all.sh
```

Binaries will be in `dist/`

### Manual Build
```bash
cd launcher
WAILS_BIN="$(go env GOPATH)/bin/wails"
"$WAILS_BIN" build -platform linux/amd64
```

## Development Mode

Hot reload for frontend changes:
```bash
cd launcher
WAILS_BIN="$(go env GOPATH)/bin/wails"
"$WAILS_BIN" dev
```

Then open Chrome DevTools with `Ctrl+Shift+I`

## Troubleshooting

### "gtk+-3.0 was not found" (Linux)
```bash
sudo apt-get install libgtk-3-dev libwebkit2gtk-4.0-dev
```

### "wails: command not found"
Wails is installed in `$(go env GOPATH)/bin/wails`. Add to PATH or use the full path.

### Build hangs on first compilation
First build takes longer due to dependencies. Be patient.

## Updating Wails Version

If you want to update Wails to a newer version:
```bash
go get github.com/wailsapp/wails/v2@latest
go mod tidy
```

Then update `go.mod` version constraint if needed.
