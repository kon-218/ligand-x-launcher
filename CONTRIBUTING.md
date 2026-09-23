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

## Repository layout

| Path | What it is |
|------|------------|
| `*.go` (root) | The Wails `main` package: app lifecycle and every method bound to the frontend |
| `internal/` | Self-contained Go packages the app uses (no Wails bindings) |
| `frontend-public/` | The shipped launcher UI (built with `-tags public`) |
| `frontend/` | Developer/operator dashboard (default build); not shipped in releases |
| `docker-compose.yml`, `docker-compose.gpu.yml`, `.env.production.template`, `config/`, `docker/` | Generated snapshot of the runtime topology from the core repository — regenerate with `make sync-runtime-topology`, never hand-edit. Release validation runs Compose from the repository root, so these stay here |
| `scripts/` | Runtime-topology sync/check, staging validation and documentation checks |
| `build/` | Icons and platform packaging assets used by Wails and the release workflow |

## Development mode

Hot reload for the frontend, Go rebuilds on backend changes:

```bash
make dev          # developer dashboard (frontend/)
make dev-public   # the shipped launcher UI (frontend-public/)
```

This provides:
- Hot reload for frontend changes
- Go rebuilds on backend changes
- Browser DevTools (`Ctrl+Shift+I`)
- Dev server at `http://localhost:34115`

The first build may take a minute on Linux due to dependencies being compiled. Subsequent rebuilds are much faster.

On Linux the Makefile adds Wails' `webkit2_41` build tag automatically when
`webkit2gtk-4.1` is installed (Ubuntu 24.04+), so no pkg-config symlink is needed.

## Testing

```bash
make test   # documentation check, then go test for both the dev and public builds
make vet
```

## Building

### Current platform

```bash
make build          # developer launcher -> build/bin/ligandx-launcher
make build-public   # public launcher    -> build/bin/ligandx
```

`make build-public` embeds the runtime-bundle signing public key; without it the
launcher refuses every runtime bundle. Override `LIGANDX_RUNTIME_PUBKEY` to test
against a different signing key.

### Specific platforms

```bash
wails build -tags public -platform windows/amd64
wails build -tags public -platform darwin/universal
wails build -tags public,webkit2_41 -platform linux/amd64
```

Cross-compilation support depends on the host; the release workflow builds each
platform natively. Wails uses a pure-Go WebView2 loader on Windows, so Linux can
also produce a portable Windows executable for testing without CGO:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags "public production" \
  -ldflags="-H windowsgui -s -w -X main.runtimeBundlePublicKeyB64=<key>" \
  -o ligandx-windows-amd64.exe .
```

Local builds are test outputs only. Release artifacts are built, signed and
published exclusively by the release workflow described below.

## Releases (CI)

Production releases are initiated only by the protected unified workflow in
the private `ligand-x-pro` repository. It supplies an immutable product version
and exact Core, Pro, and launcher SHAs to
`.github/workflows/launcher-release.yml`. Pushing `main` or a launcher tag does
not publish or refresh `latest`.

The workflow builds Linux, Windows, and macOS artifacts when launcher source
changed. For a Core-only or Pro-only release it reuses the preceding verified
launcher binaries and publishes a new signed runtime bundle. The published
assets are:

| Platform | Asset |
|----------|-------|
| Windows | `ligandx-windows-amd64.exe` |
| macOS (universal) | `ligandx-darwin-universal.dmg` |
| Linux | `ligandx-linux-amd64.AppImage` |
| Runtime bundle | `ligand-x-runtime.zip` (downloaded automatically by the launcher on first run) |
| Stable release index | `ligand-x-release-index.json` + `.sig` (drives version selection) |

> Keep the download links in [README.md](README.md) and on the website in sync with these exact asset names if the workflow output changes.

## Regenerating icons

If you update the app icon ([`build/appicon.svg`](build/appicon.svg)):

```bash
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

Ubuntu 24.04 only provides webkit2gtk-4.1. Build through the Makefile, which
passes Wails' `webkit2_41` tag, or pass `-tags webkit2_41` to `wails` yourself.

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
