# Ligand-X

**Computational drug discovery on your own hardware.**

Ligand-X runs a complete structure-based drug-discovery pipeline locally — protein prep, docking, MD, binding free energy, quantum chemistry, ADMET screening, and generative design — as a self-hosted Docker application. Everything stays on your machine: structures, trajectories, and results never leave your environment.

<p align="center">
  <img src="docs/images/ligand-x-app-ui.png" alt="Ligand-X application UI" width="920">
</p>

<p align="center">
  <a href="https://github.com/kon-218/ligand-x-launcher/releases/latest"><strong>Download the latest release →</strong></a>
  &nbsp;·&nbsp;
  <a href="https://www.ligand-x.com">Website</a>
  &nbsp;·&nbsp;
  <a href="docs/FAQ.md">FAQ</a>
  &nbsp;·&nbsp;
  <a href="https://github.com/kon-218/ligand-x-support">Support</a>
</p>

---

## What you can do with it

**Free edition** — no license required:

- **Structure Preparation** — fetch by PDB ID or import PDB/mmCIF; repair missing atoms, resolve chains, waters, and ions
- **Pocket Finding** — four independent detectors (fpocket, P2Rank, DeepPocket); ranked sites saved directly to the project
- **Molecular Docking** — AutoDock Vina single, batch, and library screens; ranked poses with interaction summaries; redocking validation
- **Molecular Dynamics** — OpenMM GPU/CPU simulation; replicate campaigns; trajectory analysis and Mol* visualization
- **Structure & Pose Alignment** — pairwise protein and multi-pose ligand alignment; RMSD outputs; direct feed into free-energy workflows
- **Molecule Editor & Library** — Ketcher 2D editor, SMILES/SDF/PDB import, batch compound loading, chemical-space map
- **Multiple Sequence Alignment** — MMseqs2, results cached by sequence hash

**Pro edition** — with a signed license:

- **ADMET Prediction** — batch SMILES screening for drug-likeness and ADMET liabilities, cached at project level
- **Boltz-2 Affinity Prediction** — AI-based binding affinity; single and batch; GPU-accelerated
- **Binding Free Energy** — ABFE (OpenFE) and RBFE with cycle-closure validation; ATM support; replicate campaigns
- **Quantum Chemistry** — DFT geometry optimization, IR/Raman spectra, conformer search, pKa (macro and micro), BDE, torsion scans, NEA UV-Vis; every QC variant runs to completion independently
- **De Novo Design** — REINVENT4 generative campaigns with scoring components and curriculum stages
- **QM/MM** *(preview)* — hybrid quantum/classical MD via OpenMM + ORCA; bring your own ORCA license
- **Unbinding Kinetics** *(preview)* — τRAMD residence-time estimation; optional NAMD acceleration

All computation runs inside isolated Docker containers. Scientific results are content-addressed, immutable, and version-tracked from submission through recovery.

---

## Install

1. Install [Docker Desktop](https://docs.docker.com/get-docker/) (Windows/macOS) or Docker Engine with Compose v2 (Linux).
2. Download the launcher for your platform from [Releases](https://github.com/kon-218/ligand-x-launcher/releases/latest).
3. Open the launcher and create a local account.
4. Continue with Free, or import a signed Academic/Pro license.
5. Select modules, choose **Download & continue**, then **Start services**.
6. Click **Open Ligand-X** — the app opens at <http://localhost:8080>.

Windows and Linux are the qualified targets. macOS builds are preview; NVIDIA-accelerated containers are not available on macOS.

**Minimum requirements:** 4-core CPU · 16 GB RAM (32 GB+ for GPU modules or large MD systems) · 50 GB free disk · Docker with Compose v2. An NVIDIA GPU is required for Boltz-2 and Binding Free Energy; optional for accelerated MD.

---

## Editions

| Edition | License | Includes |
|---------|---------|----------|
| **Free** | None | Structure prep, pocket finding, docking, MD, alignment, MSA, molecule editor |
| **Academic** | Signed academic license | Free + entitled Pro modules for non-commercial research |
| **Pro** | Signed commercial license | Free + full Pro module set per entitlement |

You can import a license at any time without reinstalling. Pro modules pull private images from the registry — no extra manual steps. Commercial use and Pro modules require commercial terms: [ligand-x.com](https://www.ligand-x.com).

---

## Security

Public builds accept runtime bundles only from allowlisted HTTPS release hosts. The launcher verifies the signed manifest, version, size, and bundle digest before extraction; rollback policy is enforced. Arbitrary `file://` overrides are disabled.

Credentials and worker secrets are generated locally. Pro registry access uses license-aware, scoped credentials. Do not publish `.env.production`, license files, registry tokens, or diagnostic output containing private paths.

Full detail including Windows code-signing status: [docs/security.md](docs/security.md).

---

## Support

Use [ligand-x-support](https://github.com/kon-218/ligand-x-support) to:

- [report a launcher problem](https://github.com/kon-218/ligand-x-support/issues/new?template=01-launcher.yml)
- [report an installation or update problem](https://github.com/kon-218/ligand-x-support/issues/new?template=04-installation.yml)
- [request a feature](https://github.com/kon-218/ligand-x-support/issues/new?template=06-feature.yml)
- [ask a question](https://github.com/kon-218/ligand-x-support/discussions)

Report security vulnerabilities through the [private advisory process](https://github.com/kon-218/ligand-x-support/security/advisories/new).

Product news: [X @LigandXinc](https://x.com/LigandXinc) · [Instagram @ligandx.inc](https://www.instagram.com/ligandx.inc/)  
Press: [social@ligand-x.com](mailto:social@ligand-x.com) · Account/licensing: [support@ligand-x.com](mailto:support@ligand-x.com)

---

## Building and contributing

Go + [Wails](https://wails.io/) v2. Public release builds use the `public` build tag and embed `frontend-public/`.

```bash
go test ./...
make dev-public
make build-public
make check-runtime-topology
```

The `frontend/` dashboard is a developer interface and is not shipped in public releases. See [CONTRIBUTING.md](CONTRIBUTING.md) for platform setup and topology sync rules.

---

## Documentation

- [FAQ](docs/FAQ.md)
- [Runtime security](docs/security.md)
- [Contributing](CONTRIBUTING.md)
- [Manual Windows build](docs/manual-windows-build.md)

---

## License

Distributed under the [PolyForm Noncommercial License 1.0.0](LICENSE). Commercial use and Pro modules require commercial terms.

Third-party Go modules are listed in [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md), attached to each GitHub release. Container images pulled by the launcher carry their own notices at `/app/THIRD_PARTY_NOTICES.md`.
