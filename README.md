# Ligand-X Launcher

The public Ligand-X desktop launcher installs and runs the local Docker-based
Ligand-X application on Windows, Linux, and macOS.

The launcher is the supported end-user entry point. It downloads a signed
runtime bundle, generates local configuration, pulls selected images, starts the
stack, and opens the web application. Core runtime images provide the Free
edition; licensed modules use private Pro images.

## Install and start

1. Install and start Docker Desktop, or Docker Engine with Compose v2 on Linux.
   See [Docker's install docs](https://docs.docker.com/get-docker/).
2. Download the launcher for your platform from
   [Releases](https://github.com/kon-218/ligand-x-launcher/releases).
3. Open the launcher and create your local account.
4. Continue with Free or import a signed Academic/Pro license.
5. Select modules, choose **Download & continue**, then **Start services**.
6. Click **Open Ligand-X** and log in with the account you created.

The app opens at <http://localhost:8080> by default (`APP_PORT`). Windows and
Linux are the qualified launcher targets in the current release process. macOS
builds are preview and cannot run NVIDIA-accelerated containers locally.

## Public launcher functionality

- Docker/runtime preflight and verified runtime installation.
- Local account setup and password change.
- Signed license import and entitlement-aware module selection.
- Image download progress with retryable errors.
- Start, stop, and open-app controls.
- CPU/GPU worker-concurrency settings.
- Optional ORCA host-folder and Boltz MSA credential settings.
- Resource-limit reset and explicit uninstall flow.
- Best-effort update notification and signed runtime replacement.

The public build intentionally uses the focused `frontend-public/` interface.
The broader dashboard under `frontend/` is a developer interface and is not a
shipped product surface; controls visible only there must not be advertised as
launcher features.

## Runtime security

Public builds accept runtime bundles only from allowlisted HTTPS release hosts.
The launcher verifies the signed manifest, version, size, and bundle digest and
enforces rollback policy before extraction. Arbitrary `file://` bundle overrides
are disabled in public builds.

Installation credentials and worker secrets are generated locally. Pro image
access uses license-aware, scoped registry credentials. Do not publish
`.env.production`, license files, registry tokens, or diagnostic output that
contains private paths or identifiers.

## Requirements

- Docker with Compose v2.
- 16 GB RAM for a small core installation; more for GPU/long-running modules.
- At least 20 GB free disk before selecting scientific images.
- NVIDIA GPU and container runtime for accelerated modules.
- Network access for initial runtime/images and optional remote providers.

See [FAQ](docs/FAQ.md) for platform and recovery guidance.

## Building and contributing

The launcher uses Go, Wails v2, and embedded HTML/CSS/JavaScript. Public release
builds compile with the `public` build tag and embed `frontend-public/`.

```bash
go test ./...
make dev-public
make build-public
make check-runtime-topology
```

The canonical Compose and production environment templates come from the sibling
core repository. Do not hand-edit launcher copies without running the topology
sync/check. See [CONTRIBUTING.md](CONTRIBUTING.md) for platform build details.

## Documentation

- [FAQ](docs/FAQ.md)
- [Contributing](CONTRIBUTING.md)
- [Manual Windows build](docs/manual-windows-build.md)
- [Setup redirect](SETUP.md)

## License

The launcher is distributed under the PolyForm Noncommercial License 1.0.0.
Commercial use and licensed Pro modules require commercial terms.
