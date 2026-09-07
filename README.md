# Ligand-X

**Computational drug discovery on your own hardware.**

Ligand-X is a self-hosted drug-discovery workbench that runs a full structure-based pipeline locally: protein preparation, molecular docking, MD simulation, binding free energy, quantum chemistry, ADMET screening, and generative design. Every structure, trajectory, and result stays on your machine.

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

## Capabilities

**Free — no license required**

| Module | What it does |
|--------|-------------|
| **Structure Preparation** | Fetch by PDB ID or import PDB/mmCIF. Repair missing atoms, resolve chains, waters, and ions. |
| **Pocket Finding** | Four independent detectors (fpocket, P2Rank, DeepPocket). Ranked binding sites saved to the project and fed directly into docking. |
| **Molecular Docking** | AutoDock Vina screens: single ligand, batch, or full library. Ranked poses with affinity scores, interaction summaries, and redocking validation. |
| **Molecular Dynamics** | OpenMM GPU/CPU simulation. Replicate campaigns with independent seeds. Trajectory analysis and Mol* visualization. |
| **Structure and Pose Alignment** | Pairwise protein alignment and multi-pose ligand series alignment. RMSD outputs and direct handoff to free-energy workflows. |
| **Molecule Editor and Library** | Ketcher 2D drawing, SMILES/SDF/PDB import, batch compound loading, and a chemical-space map of the project library. |
| **Multiple Sequence Alignment** | MMseqs2, results cached by sequence hash and reusable across projects. |

**Pro — available for academic and commercial use**

| Module | What it does |
|--------|-------------|
| **ADMET Prediction** | Batch SMILES screening for drug-likeness and ADMET liabilities. Results cached at project level. |
| **Boltz-2 Affinity Prediction** | AI-based binding affinity prediction. Single and batch. GPU-accelerated. |
| **Binding Free Energy** | ABFE (OpenFE) and RBFE with cycle-closure validation. ATM support. Replicate campaigns. |
| **Quantum Chemistry** | DFT geometry optimization, IR/Raman, conformer search, macro and micro pKa, BDE, torsion scans, NEA UV-Vis. Each variant runs to completion independently. Requires a host ORCA installation. |
| **De Novo Design** | REINVENT4 generative campaigns with scoring components and curriculum stages. |
| **QM/MM** *(preview)* | Hybrid quantum/classical MD with OpenMM and ORCA. Bring your own ORCA license. |
| **Unbinding Kinetics** *(preview)* | Residence-time estimation via τRAMD. Optional NAMD acceleration. |

All computation runs in isolated Docker containers with independently versioned conda environments. Scientific results are content-addressed and immutable.

---

## Install

1. Install [Docker Desktop](https://docs.docker.com/get-docker/) (Windows or macOS) or Docker Engine with Compose v2 (Linux).
2. Download the launcher for your platform from [Releases](https://github.com/kon-218/ligand-x-launcher/releases/latest).
3. Open the launcher and create a local account.
4. Start with Free, or import a signed license for Pro modules.
5. Select modules, choose **Download & continue**, then **Start services**.
6. Click **Open Ligand-X**. The app opens at <http://localhost:8080>.

Windows and Linux are the qualified targets. macOS builds are available in preview; NVIDIA-accelerated containers are not supported on macOS.

**Requirements:** 4-core CPU, 16 GB RAM (32 GB+ recommended for GPU modules or large MD systems), 50 GB free disk, Docker with Compose v2. An NVIDIA GPU is required for Boltz-2 and Binding Free Energy; optional for GPU-accelerated MD.

---

## Editions

| Edition | License | What you get |
|---------|---------|--------------|
| **Free** | None | Structure prep, pocket finding, docking, MD, alignment, MSA, molecule editor |
| **Academic** | Signed academic license | Free modules plus Pro capabilities for non-commercial research |
| **Pro** | Signed commercial license | Free modules plus the full Pro module set |

Licenses can be imported at any time without reinstalling the launcher or re-pulling base images.

**Academic users:** if you are using Ligand-X for non-commercial research, we offer academic licenses. Reach out at [support@ligand-x.com](mailto:support@ligand-x.com) and we will get you set up.

---

## Security

Public builds accept runtime bundles only from allowlisted HTTPS release hosts. The launcher verifies the signed manifest, version, size, and bundle digest before extraction, and enforces rollback policy. Arbitrary `file://` bundle overrides are disabled.

Credentials and worker secrets are generated locally. Pro registry access uses license-aware, scoped credentials. Do not publish `.env.production`, license files, registry tokens, or diagnostic output containing private paths.

Full detail including Windows code-signing status: [docs/security.md](docs/security.md).

---

## Support

Use [ligand-x-support](https://github.com/kon-218/ligand-x-support) to:

- [Report a launcher problem](https://github.com/kon-218/ligand-x-support/issues/new?template=01-launcher.yml)
- [Report an installation or update problem](https://github.com/kon-218/ligand-x-support/issues/new?template=04-installation.yml)
- [Request a feature](https://github.com/kon-218/ligand-x-support/issues/new?template=06-feature.yml)
- [Ask a question](https://github.com/kon-218/ligand-x-support/discussions)

Security vulnerabilities: [private advisory process](https://github.com/kon-218/ligand-x-support/security/advisories/new).

Product news: [X @LigandXinc](https://x.com/LigandXinc) and [Instagram @ligandx.inc](https://www.instagram.com/ligandx.inc/)
Press and partnerships: [social@ligand-x.com](mailto:social@ligand-x.com)
Account and licensing: [support@ligand-x.com](mailto:support@ligand-x.com)

---

## Building and contributing

Go + [Wails](https://wails.io/) v2. Public release builds use the `public` build tag and embed `frontend-public/`.

```bash
go test ./...
make dev-public
make build-public
make check-runtime-topology
```

The `frontend/` dashboard is a developer interface and is not shipped in public releases. See [CONTRIBUTING.md](CONTRIBUTING.md) for platform setup and topology sync.

---

## Documentation

- [FAQ](docs/FAQ.md)
- [Runtime security](docs/security.md)
- [Contributing](CONTRIBUTING.md)
- [Manual Windows build](docs/manual-windows-build.md)

---

## License

Distributed under the [PolyForm Noncommercial License 1.0.0](LICENSE). Commercial use and Pro modules require commercial terms.

Third-party Go modules are listed in [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md), attached to each GitHub release. Container images carry their own notices at `/app/THIRD_PARTY_NOTICES.md`.
