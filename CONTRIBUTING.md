# Contributing to the Ligand-X Launcher

This is the developer guide for building, running, and contributing to the launcher. For product info, downloads, and usage, see the [README](README.md).

The launcher is a [Wails](https://wails.io/) v2 app: a Go backend (Docker SDK integration in [`app.go`](app.go)) with a pure HTML/CSS/JS frontend (no build step, no npm, no frameworks).

## Requirements

- **Go 1.25+** (matches `go.mod`)
- **Wails CLI v2.11+**

Install the Wails CLI:

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

> The CI pins the Wails CLI to `v2.11.0` to avoid CLI/library drift. If your local library and CLI versions disagree, install the matching CLI: `go install github.com/wailsapp/wails/v2/cmd/wails@v2.11.0`.

## Platform-specific setup

### Linux (Ubuntu/Debian)

Install development libraries:

```bash
sudo apt-get update
sudo apt-get install -y libgtk-3-dev pkg-config
```

**Ubuntu 24.04 (Noble) or newer:**
```bash
sudo apt-get install -y libwebkit2gtk-4.1-dev
```

**Ubuntu 22.04 (Jammy) or older:**
```bash
sudo apt-get install -y libwebkit2gtk-4.0-dev
```

### macOS

Install Xcode Command Line Tools (one-time):
```bash
xcode-select --install
```

### Windows

No additional setup needed (uses WebView2).

## Development mode

Hot reload for the frontend, Go rebuilds on backend changes:

```bash
cd launcher
wails dev
```

Or use the convenience script:

```bash
cd launcher
./scripts/build-dev.sh
```

This provides:
- Hot reload for frontend changes
- Go rebuilds on backend changes
- Browser DevTools (`Ctrl+Shift+I`)
- Dev server at `http://localhost:34115`

The first build may take a minute on Linux due to dependencies being compiled. Subsequent rebuilds are much faster.

> On Ubuntu 24.04, you may need to create a webkit symlink first (see [Troubleshooting](#troubleshooting)).

### Testing Docker integration

```bash
cd launcher
go run . -test-docker
```

## Building

### Current platform

```bash
cd launcher
wails build
# or: ./scripts/build-current.sh
```

The binary lands in `build/bin/`.

### Specific platforms

```bash
wails build -platform windows/amd64
wails build -platform linux/amd64
wails build -platform darwin/amd64
wails build -platform darwin/arm64   # Apple Silicon
wails build -nsis                    # Windows NSIS installer
```

Cross-platform builds can also be produced with `./scripts/build-all.sh` (outputs to `dist/`).

## Releases (CI)

The recommended way to build for all platforms is via GitHub Actions
([`.github/workflows/launcher-release.yml`](.github/workflows/launcher-release.yml)):

1. Push a tag: `git tag launcher-v1.0.0 && git push origin launcher-v1.0.0`
2. GitHub Actions builds for Linux, Windows, and macOS (universal).
3. Artifacts are attached to the GitHub Release.

Pushing to `main` (or running the workflow manually without a version) refreshes the rolling `latest` release. The published assets are:

| Platform | Asset |
|----------|-------|
| Windows | `ligandx-windows-amd64.exe` |
| macOS (universal) | `ligandx-darwin-universal.dmg` |
| Linux | `ligandx-linux-amd64.AppImage` |
| Runtime bundle | `ligand-x-runtime.zip` (downloaded automatically by the launcher on first run) |

> Keep the download links in [README.md](README.md) and on the website in sync with these exact asset names if the workflow output changes.

## Regenerating icons

If you update the app icon ([`build/appicon.svg`](build/appicon.svg)):

```bash
cd launcher

# From SVG source (requires ImageMagick)
convert -background none build/appicon.svg -resize 1024x1024 build/appicon.png
convert -background none build/appicon.svg -resize 1024x1024 build/darwin/appicon.png
convert -background none build/appicon.svg -resize 256x256 \
  -define icon:auto-resize=256,128,64,48,32,16 build/windows/icon.ico

# Or use Wails to generate from PNG
wails generate icons build/appicon.png
```

## Troubleshooting (build)

#### "webkit2gtk-4.0 was not found" (Ubuntu 24.04)

Ubuntu 24.04 only provides webkit2gtk-4.1, but Wails v2.11 is hardcoded to look for 4.0. Create a symlink:

```bash
sudo ln -s /usr/lib/x86_64-linux-gnu/pkgconfig/webkit2gtk-4.1.pc \
           /usr/lib/x86_64-linux-gnu/pkgconfig/webkit2gtk-4.0.pc
```

**Alternative:** use the GitHub Actions CI to build (it handles this automatically).

#### "gtk+-3.0 was not found" (Linux)

Install the development libraries listed in [Platform-specific setup](#platform-specific-setup):

```bash
# Ubuntu 24.04 (Noble)
sudo apt-get install -y libgtk-3-dev libwebkit2gtk-4.1-dev pkg-config
# Ubuntu 22.04 (Jammy) or older
sudo apt-get install -y libgtk-3-dev libwebkit2gtk-4.0-dev pkg-config
```

#### "wails: command not found"

The Wails CLI is installed to `$(go env GOPATH)/bin/wails`. Either:
- Add it to PATH: `export PATH="$PATH:$(go env GOPATH)/bin"`, or
- Use the full path: `$(go env GOPATH)/bin/wails dev`

#### Build hangs on first compilation

The first build takes longer because dependencies are compiled from scratch. Subsequent builds are much faster.

## Updating Wails

```bash
go get github.com/wailsapp/wails/v2@latest
go mod tidy
```

Then update the `WAILS_VERSION` pin in the release workflow if needed.

## Resources

- [Wails docs](https://wails.io/)
- [Go Docker SDK](https://pkg.go.dev/github.com/docker/docker)
- [GitHub Actions](https://docs.github.com/actions)
