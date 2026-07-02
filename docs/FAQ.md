# Frequently asked questions

General questions about installing and running Ligand-X with the launcher. For
build/development questions, see [CONTRIBUTING.md](../CONTRIBUTING.md). For product
info and downloads, see the [README](../README.md).

## Editions & licensing

### Which edition do I need: Free, Academic, or Pro?


| Edition      | You need                         | You get                                                                                                                                                    |
| ------------ | -------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Free**     | No license file                  | The full open-core workbench: projects, molecule library, Ketcher editing, Mol* viewing, protein cleaning, pocket finding, docking, MD, and MSA/alignment. |
| **Academic** | A signed academic license file   | Everything in Free **plus all Pro modules** (QC, ADMET, Boltz-2, ABFE/RBFE, GenAI). Free for qualifying academic use.                                      |
| **Pro**      | A signed commercial license file | Free plus the paid Pro entitlements listed in your license. Required for commercial use.                                                                   |


Start with Free. You can import a license later without reinstalling. [Compare Free and Pro](https://www.ligand-x.com/#pro) or [contact us](https://www.ligand-x.com/#contact) for an academic or commercial license.

### How do I import a license?

Open the launcher and use the license import step in the setup wizard (or the license panel). The file is a signed license; the launcher verifies it before unlocking the corresponding modules.

### Can I use Ligand-X commercially?

The public repository is licensed **PolyForm Noncommercial**. Commercial use and Pro modules require a Ligand-X Pro license. [Contact us](https://www.ligand-x.com/#contact) for commercial terms.

## Hardware & GPU

### Do I need a GPU?

No. The open-core workbench (docking, MD on CPU, structure prep, and so on) runs without a GPU required, only as optional execution platform. A GPU is only required for GPU-accelerated modules such as **Boltz-2** and **ABFE/RBFE**.

### How do I enable GPU acceleration?

- **Linux:** install the [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/install-guide.html).
- **Windows:** run Docker Desktop with the WSL 2 backend and set up [WSL 2 GPU passthrough](https://docs.nvidia.com/cuda/wsl-user-guide/index.html) + the NVIDIA Container Toolkit for WSL 2.
- **macOS:** NVIDIA GPU acceleration is not available; GPU-only modules won't run.

Only NVIDIA GPUs are supported for accelerated modules.

## Installation & networking

### Can I install offline / behind a firewall?

The launcher normally downloads `ligand-x-runtime.zip` and the selected Docker images from the internet on first run. For offline or air-gapped installs, point it at a local bundle:

```bash
export LIGANDX_RUNTIME_BUNDLE_URL=file:///path/to/ligand-x-runtime.zip
```

You'll still need the Docker images available locally (pre-pulled or mirrored in an internal registry).

### Which ports does Ligand-X use?

Make sure these are free before starting:


| Port | Service               |
| ---- | --------------------- |
| 3000 | Frontend (the app UI) |
| 8000 | API gateway           |
| 5432 | PostgreSQL database   |
| 6379 | Redis                 |
| 5672 | RabbitMQ              |


If a port is in use, stop the conflicting process or the previous Ligand-X stack, then start again.

### Where does the launcher store data?

Runtime files are stored in your user config directory by default. To use a custom location, set `LIGANDX_RUNTIME_DIR` before launching. Advanced users can also point the launcher at a source checkout containing `docker-compose.yml` via the folder path in the footer.

## Running & updating

### How do I open the app?

After **Install & Start**, the launcher opens your browser at `[http://localhost:3000](http://localhost:3000)` automatically. You can also click **Open App** in the launcher.

### How do I update to a new version?

Download the latest launcher from [Releases](https://github.com/kon-218/ligand-x-launcher/releases) and replace your existing binary/app. The launcher manages runtime files and image updates on start.

### How do I uninstall?

Stop the stack from the launcher (or with **Clean** to remove Docker resources), then delete the launcher binary/app and the runtime directory. Removing Docker images is optional (`docker image prune` / remove the `ghcr.io/kon-218/ligand-x`* images).

## Troubleshooting

### macOS says the app is from an "unidentified developer"

The app isn't code-signed with an Apple Developer certificate. Right-click the app → **Open** → **Open**, or allow it under System Settings → Privacy & Security → **Open Anyway**.

### Windows SmartScreen warns about the .exe

The binary is unsigned. Click **More info → Run anyway**. No admin rights are required.

### The Linux AppImage won't launch

Ensure FUSE is installed and the file is executable:

```bash
sudo apt-get install libfuse2   # Ubuntu/Debian
chmod +x ligandx-linux-amd64.AppImage
./ligandx-linux-amd64.AppImage
```

### Services won't start

Check the launcher's logs panel, confirm the [required ports](#which-ports-does-ligand-x-use) are free, and try **Clean** to clear stale Docker resources. Make sure Docker is running (on Linux your user must be in the `docker` group).

---

Still stuck? [Contact us](https://www.ligand-x.com/#contact) and include logs from the launcher's **Diagnostics** panel.