# Ligand-X

**Computational drug discovery on your own hardware.**

Ligand-X is a self-hosted drug-discovery platform that runs locally on your own machine. It covers the full structure-based pipeline: protein preparation, molecular docking, MD simulation, ADMET screening, binding free energy, quantum chemistry, and generative design. Your structures, trajectories, and results never leave your environment.

<p align="center">
  <img src="docs/images/ligand-x-app-ui.png" alt="Ligand-X application UI" width="920">
</p>

<p align="center">
  <a href="https://github.com/kon-218/ligand-x-launcher/releases/latest"><strong>Download</strong></a>
  &nbsp;·&nbsp;
  <a href="https://www.ligand-x.com">Website</a>
  &nbsp;·&nbsp;
  <a href="docs/FAQ.md">FAQ</a>
  &nbsp;·&nbsp;
  <a href="https://github.com/kon-218/ligand-x-support">Support</a>
</p>

## Getting started

1. Install [Docker Desktop](https://docs.docker.com/get-docker/) (Windows or macOS) or Docker Engine with Compose v2 (Linux).
2. Download the launcher for your platform from [Releases](https://github.com/kon-218/ligand-x-launcher/releases/latest).
3. Open the launcher and create a local account.
4. Start with Free, or import a signed license for Pro modules.
5. Select modules, choose **Download & continue**, then **Start services**.
6. Click **Open Ligand-X**. The app opens at <http://localhost:8080>.

Windows and Linux are the qualified targets. macOS builds are available in preview — NVIDIA-accelerated containers are not supported on macOS.

**Requirements:** 4-core CPU, 16 GB RAM (32 GB+ for GPU modules or large MD systems), 50 GB free disk, Docker with Compose v2. An NVIDIA GPU is required for Boltz-2 and Binding Free Energy, and optional for GPU-accelerated MD.

## What's included

**Free** (no license needed): structure preparation, pocket finding, molecular docking (AutoDock Vina), molecular dynamics (OpenMM), structure and pose alignment, molecule editor and library, multiple sequence alignment.

**Pro**: ADMET prediction, Boltz-2 binding affinity, binding free energy (ABFE/RBFE via OpenFE), quantum chemistry (DFT, pKa, BDE, IR, conformers, NEA UV-Vis via ORCA), de novo design (REINVENT4), QM/MM *(preview)*, and unbinding kinetics *(preview)*.

All modules run in isolated Docker containers with independently versioned conda environments.

## Editions and licensing

| Edition | How to get it |
|---------|--------------|
| Free | Just download and run |
| Academic | Email us — we offer academic licenses for non-commercial research |
| Pro | Commercial license from [ligand-x.com](https://www.ligand-x.com) |

Licenses can be imported at any time without reinstalling. If you are using Ligand-X in an academic lab, email [support@ligand-x.com](mailto:support@ligand-x.com) and we will sort out a license.

## Support

- [Report a problem or request a feature](https://github.com/kon-218/ligand-x-support/issues)
- [Ask a question](https://github.com/kon-218/ligand-x-support/discussions)
- [Security vulnerabilities](https://github.com/kon-218/ligand-x-support/security/advisories/new) (private)

Product news: [X @LigandXinc](https://x.com/LigandXinc) and [Instagram @ligandx.inc](https://www.instagram.com/ligandx.inc/)
Press and partnerships: [social@ligand-x.com](mailto:social@ligand-x.com)
Account and licensing: [support@ligand-x.com](mailto:support@ligand-x.com)

## Security

The launcher only accepts runtime bundles from allowlisted HTTPS release hosts and verifies the signed manifest, version, size, and bundle digest before extraction. Credentials and worker secrets are generated locally. Do not publish `.env.production`, license files, or registry tokens.

More detail including Windows code-signing status: [docs/security.md](docs/security.md).

## Building

Go + [Wails](https://wails.io/) v2. Public release builds use the `public` build tag.

```bash
go test ./...
make dev-public
make build-public
make check-runtime-topology
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for platform setup. The `frontend/` dashboard is a developer interface and is not shipped in public releases.

## Documentation

- [FAQ](docs/FAQ.md)
- [Runtime security](docs/security.md)
- [Contributing](CONTRIBUTING.md)
- [Manual Windows build](docs/manual-windows-build.md)

## License

Distributed under the [PolyForm Noncommercial License 1.0.0](LICENSE). Commercial use requires commercial terms.

Third-party Go modules are listed in [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md). Container images carry their own notices at `/app/THIRD_PARTY_NOTICES.md`.
