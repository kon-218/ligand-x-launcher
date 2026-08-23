# Ligand-X Launcher

Desktop installer for Ligand-X on Windows, Linux, and macOS. It downloads a signed
runtime, pulls the images you select, starts the Docker stack, and opens the app
in your browser.

Core images cover the Free edition. Academic and Pro modules need a signed
license and private images.

<p align="center">
  <img src="docs/images/ligand-x-app-ui.png" alt="Ligand-X application UI" width="920">
</p>

<p align="center">
  <a href="https://github.com/kon-218/ligand-x-launcher/releases/latest"><strong>Download the latest release</strong></a>
  ·
  <a href="https://www.ligand-x.com">Website</a>
  ·
  <a href="docs/FAQ.md">FAQ</a>
</p>

## Features

- Docker/runtime preflight and verified runtime install
- Local account setup
- Signed license import and module selection by entitlement
- Image download progress with retryable errors
- Start, stop, and open-app controls
- CPU/GPU worker concurrency settings
- Optional ORCA host folder and Boltz MSA credentials
- Resource-limit reset and uninstall
- Update notification, signed stable-version selection, and verified runtime replacement (no arbitrary image tags from the UI)

Windows and Linux are the qualified targets. macOS builds are preview and cannot
run NVIDIA-accelerated containers locally.

## Install and start

1. Install and start [Docker Desktop](https://docs.docker.com/get-docker/), or Docker Engine with Compose v2 on Linux.
2. Download the launcher for your platform from
   [Releases](https://github.com/kon-218/ligand-x-launcher/releases/latest).
3. Open the launcher and create your local account.
4. Continue with Free, or import a signed Academic/Pro license.
5. Select modules, choose **Download & continue**, then **Start services**.
6. Click **Open Ligand-X** and log in with the account you created.

The app opens at <http://localhost:8080> by default (`APP_PORT`).

## Requirements

- Docker with Compose v2
- 16 GB RAM for a small core installation; more for GPU or long-running modules
- At least 20 GB free disk before selecting scientific images
- NVIDIA GPU and container runtime for accelerated modules
- Network access for the initial runtime and images

See the [FAQ](docs/FAQ.md) for platform and recovery guidance.

## Editions

| Edition | How you start | What it unlocks |
| ------- | ------------- | --------------- |
| **Free** | No license file | Core runtime modules |
| **Academic** | Signed academic license | Entitlements in that license |
| **Pro** | Signed commercial license | Entitlements in that license |

You can import a license later without reinstalling the launcher. You may need to
pull extra private images. Commercial use and Pro modules need commercial terms
([website](https://www.ligand-x.com)).

## Security

Public builds only accept runtime bundles from allowlisted HTTPS hosts. The
launcher checks the signed manifest, version, size, and digest before install.
Credentials are generated locally; Pro registry access follows your license.

More detail (including Windows signing status): [Runtime security](docs/security.md).

## Support and feedback

Use the official [Ligand-X Support](https://github.com/kon-218/ligand-x-support) repository to:

- [report a launcher problem](https://github.com/kon-218/ligand-x-support/issues/new?template=01-launcher.yml);
- [report an installation or update problem](https://github.com/kon-218/ligand-x-support/issues/new?template=04-installation.yml);
- [request a feature](https://github.com/kon-218/ligand-x-support/issues/new?template=06-feature.yml); or
- [ask a usage question](https://github.com/kon-218/ligand-x-support/discussions).

Do not publish licence keys, credentials, proprietary structures, or confidential logs. Security
vulnerabilities must be submitted through the support repository's
[private reporting process](https://github.com/kon-218/ligand-x-support/security/advisories/new).

## Building and contributing

Go + [Wails](https://wails.io/) v2. Public release builds use the `public` build
tag and embed `frontend-public/`.

```bash
go test ./...
make dev-public
make build-public
make check-runtime-topology
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup and topology sync. The
`frontend/` dashboard is a developer UI and is not shipped in public releases.

## Documentation

- [FAQ](docs/FAQ.md)
- [Runtime security](docs/security.md)
- [Contributing](CONTRIBUTING.md)
- [Manual Windows build](docs/manual-windows-build.md)
- [README image assets](docs/images/README.md)

## License

Distributed under the [PolyForm Noncommercial License 1.0.0](LICENSE).
Commercial use and licensed Pro modules require commercial terms.
