# Ligand-X Launcher - Setup Complete

## What Was Created

A production-ready **Wails application** for launching Ligand-X with a modern GUI instead of terminal commands.

```
launcher/
├── main.go                      # Wails app entry point
├── app.go                       # Go backend (Docker SDK integration)
├── wails.json                   # Wails configuration
├── go.mod / go.sum              # Go dependencies
├── README.md                    # User documentation
├── CONTRIBUTING.md              # Developer setup guide
├── SETUP.md                     # This file
├── .gitignore                   # Git ignore patterns
├── frontend/
│   ├── index.html              # Modern dark-themed UI
│   ├── style.css               # Tailwind-inspired CSS
│   └── app.js                  # Frontend logic
└── scripts/
    ├── build-current.sh        # Build for current OS
    └── build-all.sh            # Cross-platform builds
```

## Installation Status

✅ Wails CLI installed: `v2.11.0`
✅ Go dependencies downloaded: `go mod tidy`
✅ GitHub Actions workflow configured

## Quick Start

### Building Locally

```bash
cd launcher
./scripts/build-current.sh
```

Output: `launcher/build/bin/ligandx-launcher` (Linux), `.exe` (Windows), `.app` (macOS)

### For Linux Users

Install dependencies first:
```bash
sudo apt-get install libgtk-3-dev libwebkit2gtk-4.0-dev pkg-config
```

Then build:
```bash
cd launcher
./scripts/build-current.sh
```

## Release Workflow (GitHub)

### Create a Release

1. Tag the version:
   ```bash
   git tag launcher-v1.0.0
   git push origin launcher-v1.0.0
   ```

2. GitHub Actions automatically:
   - Builds for Linux, Windows, macOS (Intel & ARM)
   - Attaches binaries to the GitHub Release
   - Creates a release page

3. Users download from: `https://github.com/YOUR_ORG/ligand-x/releases`

### Manually Trigger a Build

Go to **Actions** → **Build Launcher** → **Run workflow**

## Architecture

### Backend (Go)
- **Docker SDK integration** - Controls containers programmatically
- **Service detection** - Finds running Ligand-X services
- **Log streaming** - Real-time container logs
- **Error handling** - User-friendly messages

### Frontend (Web)
- **Pure HTML/CSS/JS** - No build step, no npm
- **Wails bindings** - Calls Go functions from JS
- **Event-driven** - Streams logs via WebSocket-like EventsOn
- **Modern UI** - Dark theme, responsive layout

### Packaging
- **Native binaries** - Single .exe, .app, or binary file
- **Small size** - ~10-15MB (vs Electron's 150MB+)
- **Fast startup** - Uses OS WebView (Safari, Edge, WebKitGTK)

## Key Features

| Feature | How It Works |
|---------|------------|
| **Service Status** | Docker SDK queries containers every 5s |
| **Start/Stop** | Runs `docker compose up/down` in project directory |
| **Logs** | Streams `docker compose logs` in real-time |
| **Open App** | Browser detection + launch `localhost:3000` |
| **Project Detection** | Searches for `docker-compose.yml` up the file tree |

## Go Backend Methods (Called from Frontend)

```go
// Docker operations
StartServices(mode string) error    // mode: dev, prod, core, docking, md
StopServices() error
RestartServices() error
PullImages() error
CleanDocker() error

// Status & monitoring
GetSystemStatus() SystemStatus
CheckDocker() (bool, string)

// Browser shortcuts
OpenFrontend()  // localhost:3000
OpenAPI()       // localhost:8000/docs
OpenFlower()    // localhost:5555/flower

// Project management
GetProjectPath() string
SetProjectPath(path string) error
SelectProjectFolder() (string, error)

// Logs
ViewLogs(service string) error
StopLogStream(service string)
```

## Frontend Components

| Component | Purpose |
|-----------|---------|
| Status Panel | Shows running services count |
| Controls Panel | Start/stop buttons + mode selector |
| Quick Links | Open app, API, Flower |
| Logs Panel | Real-time log streaming |
| Footer | Project path, cleanup button |

## Next Steps

1. **Commit to git** (Go, frontend, and config files only - NOT binaries)
2. **Tag first release**: `git tag launcher-v1.0.0 && git push origin launcher-v1.0.0`
3. **GitHub Actions builds** - Check Actions tab
4. **Download from Releases** - Users grab pre-built binaries

## Troubleshooting

### Linux build fails with "gtk+-3.0 not found"
```bash
sudo apt-get install libgtk-3-dev libwebkit2gtk-4.0-dev pkg-config
```

### "wails: command not found"
The CLI is installed to `$(go env GOPATH)/bin/wails`. Either:
- Add to PATH: `export PATH="$PATH:$(go env GOPATH)/bin"`
- Or use full path in scripts

### Windows can't find docker
- Docker Desktop must be running
- Or Docker Engine (Linux)

## Resources

- **Wails Docs**: https://wails.io/
- **Go Docker SDK**: https://pkg.go.dev/github.com/docker/docker
- **GitHub Actions**: https://docs.github.com/actions

---

Ready to release! Just tag a version and let GitHub Actions handle the builds.
