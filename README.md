



# Ligand-X

### Integrated. Self-hosted. Reliable.

**A free, self-hosted desktop app for computational drug discovery. Docking, MD, and more, all running on your own hardware.**

[Latest release](https://github.com/kon-218/ligand-x-launcher/releases/latest)
[Build status](https://github.com/kon-218/ligand-x-launcher/actions/workflows/launcher-release.yml)
Platforms
License
[Download](https://github.com/kon-218/ligand-x-launcher/releases/latest)
[Website](https://www.ligand-x.com)

**[Download](#download)**  ·  [Quickstart](#quickstart)  ·  [Features](#features)  ·  [Editions](#editions--licensing)  ·  [Website](https://www.ligand-x.com)



  
*Install Docker, open the launcher, and click Install & Start. Ligand-X opens in your browser.*

---

## Download

Grab the latest launcher for your platform. You'll need Docker installed and running first (see [Prerequisites](#prerequisites)).


| Platform                          | Download                                                                                                                           | Notes                                                      |
| --------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------- |
| **Windows** (64-bit)              | [ligandx-windows-amd64.exe](https://github.com/kon-218/ligand-x-launcher/releases/latest/download/ligandx-windows-amd64.exe)       | Portable. Double-click to run, no install or admin needed. |
| **macOS** (Intel + Apple Silicon) | [ligandx-darwin-universal.dmg](https://github.com/kon-218/ligand-x-launcher/releases/latest/download/ligandx-darwin-universal.dmg) | Open the DMG, drag the app to Applications.                |
| **Linux** (64-bit)                | [ligandx-linux-amd64.AppImage](https://github.com/kon-218/ligand-x-launcher/releases/latest/download/ligandx-linux-amd64.AppImage) | Run `chmod +x`, then double-click or run it.               |


Want to compare every install option (desktop and server) in your browser? See the [Download page on ligand-x.com](https://www.ligand-x.com/#download).

---

## What is Ligand-X?

Ligand-X is a computational drug-discovery workbench that runs entirely on hardware you control. The launcher installs, configures, and runs the whole stack as Docker services, so there's no terminal, no `docker compose` commands, and no cloud upload of sensitive structures.

- **One-click install and run.** Download a single binary and click Install & Start. The launcher pulls the images, writes local config, and boots the app for you.
- **Fine-grained, licensed modules.** Start with the open-core workbench for free, then unlock Pro modules with an Academic or Pro license when you need them.
- **Built-in diagnostics.** Live service status, logs, and cleanup tools are right there when something needs troubleshooting.

Built with open source tools  ·  Runs locally on your hardware  ·  Always free for academics

### Who it's for


|                            |                                                                                                                  |
| -------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| **Academic lab**           | Run teaching, docking, MD, and local project workflows without managed cloud infrastructure.                     |
| **Startup discovery team** | Keep early target and ligand work private while standardizing project assets and computational jobs.             |
| **Computational chemist**  | Go from cleaned structures to docked poses to MD trajectories without stitching separate tools together by hand. |


---

## Screenshots


|                                                         |                                                                 |
| ------------------------------------------------------- | --------------------------------------------------------------- |
| **First-run setup.** Guided install with no terminal.   | **Module selection.** Pick open-core and licensed Pro services. |
| **Service monitoring.** Live status of every container. | **Diagnostics.** Real-time logs and cleanup tools.              |


---

## Quickstart

1. **Install Docker.** Use [Docker Desktop](https://www.docker.com/products/docker-desktop/) on Windows/macOS or [Docker Engine + Compose v2](https://docs.docker.com/engine/install/) on Linux, and make sure it's running.
2. **Download and open the launcher.** Pick your platform from the [Download](#download) section above.
3. **Click Install & Start.** Import a license (or continue with Free), choose your modules, and start the stack. Ligand-X opens automatically at [http://localhost:3000](http://localhost:3000).

---

## Features

- **First-run install.** Downloads the Ligand-X runtime files for you, so there's no git clone.
- **License import.** Verifies Free, Academic, and Pro licenses before unlocking modules.
- **One-click start/stop.** No terminal or Docker Compose commands needed.
- **Selective module downloads.** Pull only the open-core and licensed Pro services you actually select.
- **Service monitoring.** Real-time status of all Docker containers.
- **Diagnostics.** Advanced logs and cleanup tools for support cases.

### Start modes


| Mode           | Description                                                        |
| -------------- | ------------------------------------------------------------------ |
| Production     | Full stack from published images. The default for installed users. |
| Development    | Source checkout with hot reload, intended for contributors.        |
| Core Only      | Gateway, Frontend, Structure, Database, Redis.                     |
| Core + Docking | Core plus the docking service and CPU workers.                     |
| Core + MD      | Core plus the MD service and GPU workers.                          |


---

## How it works

The launcher is a small native desktop app, built with [Wails](https://wails.io/) (Go plus your OS's WebView). It talks directly to the Docker Engine to pull and run the Ligand-X platform, which ships as container images.

```
Ligand-X Launcher  ──▶  Docker Engine  ──▶  Gateway · Frontend · Structure · Docking · MD · Database · GPU workers
```

By default the launcher stores runtime files in your user config directory. If you need a custom deployment, point it at a source checkout or set `LIGANDX_RUNTIME_DIR` / `LIGANDX_RUNTIME_BUNDLE_URL`.

Running on a server or a headless machine? The same platform installs with Docker Compose directly. See the [server install path](https://www.ligand-x.com/#download).

---

## Editions & licensing

Ligand-X is open core. The everyday local workbench is free, and the advanced decision modules unlock with a license.


| Edition      | License             | Access                                          |
| ------------ | ------------------- | ----------------------------------------------- |
| **Free**     | No license file     | Core modules only                               |
| **Academic** | Signed license file | All Pro modules                                 |
| **Pro**      | Signed license file | The paid Pro entitlements listed in the license |


**Open core** covers projects, the molecule library, Ketcher editing, Mol* viewing, protein cleaning, pocket finding, docking, MD, and MSA/alignment.

**Pro** adds QC, ADMET, Boltz-2, ABFE/RBFE, and GenAI for property risk, binding confidence, and generative design.

The public repository is licensed PolyForm Noncommercial. Commercial use and Pro modules require a Ligand-X Pro license, so [compare Free and Pro](https://www.ligand-x.com/#pro) if you're not sure which you need.

---

## Security & privacy

- **Runs locally.** The platform executes on your own CPU/GPU, via Docker on your machine.
- **Your data stays put.** Structures, ligands, jobs, and results live in your local workspace. There's no required cloud upload for sensitive structures.
- **Transparent licensing.** Licenses are signed files that you import, and the Free edition needs none.

---

## Prerequisites

1. **Docker.** [Docker Desktop](https://www.docker.com/products/docker-desktop/) on Windows/macOS, or [Docker Engine](https://docs.docker.com/engine/install/) on Linux. It must be running.
2. **Docker Compose** v2.0+ (bundled with Docker Desktop).
3. **NVIDIA GPU** *(optional)*, required for Boltz-2 and ABFE/RBFE. Install the [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/install-guide.html) on Linux, or enable GPU in Docker Desktop settings on Windows/macOS.

---

## Troubleshooting

#### Docker not running

- Make sure Docker Desktop is running (or the Docker daemon on Linux).
- On Linux, add your user to the `docker` group: `sudo usermod -aG docker $USER`.

#### "Runtime files are not installed"

- Click **Install & Start** in the setup wizard. The launcher downloads `ligand-x-runtime.zip` from the latest release.
- For offline installs, set `LIGANDX_RUNTIME_BUNDLE_URL=file:///path/to/ligand-x-runtime.zip` before starting the launcher.
- Advanced users can click the folder path in the footer and select a source checkout that contains `docker-compose.yml`.

#### Services not starting

- Check the logs panel for error messages.
- Make sure ports 3000, 8000, 5432, 6379, and 5672 are free.
- Try **Clean** to remove stale Docker resources.

#### macOS "unidentified developer" warning

The app isn't code-signed with an Apple Developer certificate. To open it:

- Right-click the app, choose **Open**, then click **Open** in the dialog, or
- Go to System Settings, then Privacy & Security, and click **Open Anyway**.

#### Linux AppImage won't run

Make sure FUSE is installed and the file is executable:

```bash
sudo apt-get install libfuse2   # Ubuntu/Debian
chmod +x ligandx-linux-amd64.AppImage
./ligandx-linux-amd64.AppImage
```

---

## Support & contact

- **FAQ:** [docs/FAQ.md](docs/FAQ.md) covers editions, GPU, offline install, ports, and common fixes.
- **Docs:** [ligand-x.com/#docs](https://www.ligand-x.com/#docs)
- **Downloads & releases:** [GitHub Releases](https://github.com/kon-218/ligand-x-launcher/releases)
- **Sales, demos & enterprise:** [Contact us](https://www.ligand-x.com/#contact)
- **Support cases:** open the launcher's **Diagnostics** panel to collect logs, then include them with your request.

---

## Contributing & building from source

This README is the product front page. If you want to build the launcher, run it in development mode, or contribute, see [CONTRIBUTING.md](CONTRIBUTING.md).

## License

The public Ligand-X launcher uses the PolyForm Noncommercial license, the same as the main Ligand-X project. Commercial use and Pro modules require a Ligand-X Pro license.

---

[Website](https://www.ligand-x.com) · [Docs](https://www.ligand-x.com/#docs) · FAQ · [Releases](https://github.com/kon-218/ligand-x-launcher/releases) · [Contributing](CONTRIBUTING.md) · [Pro](https://www.ligand-x.com/#pro)