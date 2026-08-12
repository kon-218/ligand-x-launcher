# Runtime security

Public Ligand-X Launcher builds accept runtime bundles only from allowlisted
HTTPS release hosts. Before extraction, the launcher verifies the signed
manifest, version, size, and bundle digest, and enforces rollback policy.
Arbitrary `file://` bundle overrides are disabled in public builds.

The signed runtime manifest records platform-signing evidence separately from
artifact hashes. Windows builds may be distributed without Authenticode until a
Microsoft-trusted certificate is available; release notes disclose that state
and Windows clean-install qualification remains required. Users should expect a
SmartScreen warning for an unsigned build.

Installation credentials and worker secrets are generated locally. Pro image
access uses license-aware, scoped registry credentials.

## Operational hygiene

Do not publish:

- `.env.production`
- license files
- registry tokens
- diagnostic output that contains private paths or identifiers

For offline or air-gapped deployment constraints, see the [FAQ](FAQ.md).
