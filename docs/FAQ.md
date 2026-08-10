# Launcher FAQ

## Which edition should I install?

- Free requires no license file and provides the core runtime modules.
- Academic uses a signed academic license and the entitlements it grants.
- Pro uses a signed commercial license and its listed entitlements.

Importing a license later does not require reinstalling the launcher. It may
require pulling additional private images.

## Do I need a GPU?

Not for the smallest core topology. An NVIDIA GPU and working NVIDIA container
runtime are required or strongly recommended for accelerated MD, Boltz-2,
free-energy, and other GPU worker paths. macOS cannot provide NVIDIA container
acceleration.

## Where are runtime files stored?

The launcher uses the platform user-config directory unless
`LIGANDX_RUNTIME_DIR` is set in a controlled deployment. Results and databases
live in Docker volumes/runtime directories, not in the launcher executable.

## Can the public launcher install an offline local bundle?

No. Public builds reject arbitrary local and `file://` runtime-bundle URLs. They
download from approved HTTPS release hosts and verify a signed manifest and
digest. Offline/air-gapped deployment requires a separately controlled build or
an approved internal distribution process; copying an unsigned ZIP into the
public launcher is intentionally unsupported.

## Which ports are used?

The normal browser endpoints are frontend port 3000 and gateway port 8000. The
Compose network also uses PostgreSQL 5432, Redis 6379, RabbitMQ 5672, and
internal service ports. Do not expose database, broker, Redis, or service ports
to an untrusted network.

## Why is a module unavailable?

Check all four conditions:

1. The installed runtime version contains the module.
2. The license grants its entitlement.
3. Preview/experimental modules are explicitly enabled where required.
4. The machine satisfies engine/GPU requirements and the image pull succeeded.

An API or settings field can exist while the corresponding preview capability is
disabled.

## Why did runtime installation fail?

Open the install log shown by the launcher. Common causes include Docker not
running, insufficient disk, network/rate-limit errors, an invalid signature or
digest, a blocked rollback, or incomplete extraction. The previous installed
runtime is retained until verified replacement succeeds.

## Why will services not start?

- Confirm Docker is running and ports 3000/8000 are free.
- Confirm selected images finished downloading.
- Check GPU availability for selected GPU modules.
- Reduce worker concurrency/resource settings on smaller machines.
- Use **Reset to defaults** if manual resource edits prevent startup.

The public launcher displays installation/start errors but does not ship the
developer dashboard’s broad log-filter/export controls. Use the emitted setup log
and Docker/Compose diagnostics for a support case.

## How do updates work?

The launcher checks the release source on a best-effort basis. **Update now**
downloads and verifies the new runtime before advancing the local version.
Network failure or rate limiting should not prevent use of the already-installed
runtime.

## How do I back up results?

Use the versioned backup command from the installed runtime before uninstalling
or deleting Docker volumes. A qualified restore uses isolated staging and an
explicit promotion; do not restore a plain SQL dump into the active database.

## What does uninstall remove?

The launcher requires explicit confirmation because uninstall can stop and
remove Ligand-X containers, images, stored results, runtime files, and the
launcher. Back up required results first. The exact confirmation screen is the
authority for the current build.

## Platform warnings

- macOS preview builds may require **Open anyway** because signing/notarization is
  not yet part of the stable path.
- Windows preview artifacts may trigger SmartScreen when unsigned.
- Linux AppImage execution may require FUSE and executable permissions.

For source builds, see [CONTRIBUTING.md](../CONTRIBUTING.md).
