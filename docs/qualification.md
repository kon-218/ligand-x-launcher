# Isolated launcher-path qualification

`qualification_harness_test.go` is excluded from ordinary builds and tests by
the `qualification` build tag. It exercises verified runtime-bundle installation,
image-pinned grouped start and upgrade through the desktop application's code.

The driver must provide a unique `lx-qualification-*` Compose project, separate
runtime and launcher-config directories, signed bundle URL/version/public key,
launcher compatibility version, immutable image digest map, selected service
groups and an isolated environment file. The runtime bundle's signature is
verified before installation; a source checkout must not replace installation.

Required environment controls include:

- `LIGANDX_QUALIFICATION_OPT_IN=I_ACCEPT_ISOLATED_RUNTIME_CHANGES`
- `LIGANDX_QUALIFICATION_PROJECT`
- `LIGANDX_RUNTIME_DIR` and `LIGANDX_LAUNCHER_CONFIG_DIR`
- `LX_BUNDLE_URL`, `LX_VERSION`, `LX_PUBLIC_KEY`, `LX_LAUNCHER_VERSION`
- `LX_DIGESTS_FILE`, `LX_ENV_FILE`, `LX_GROUPS`, `LX_PHASE_OUTPUT`
- `LX_BASELINE=1` for a historical bundle; it starts only services that bundle declares
- `LX_LICENSE` for licensed groups

The harness writes a temporary Compose overlay that pins first-party images by
digest. It keeps the installation's database/output data across upgrade phases.
Cleanup must use the same unique project, environment and overlay; qualification
must never use a customer's runtime, config directory or default Compose project.

Normal source verification is `make test`. The isolation tests can run without
installing a bundle:

```bash
go test -tags qualification -run '^TestQualification' ./...
```

Actual runtime qualification is a separate opt-in run of
`TestLauncherPathAcceptance` with all inputs present. Source tests and compilation
do not prove a packaged desktop or candidate image. Runtime evidence must name
the bundle, launcher revision, image digests and paths exercised.
