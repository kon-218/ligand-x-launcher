# Manual Windows launcher verification build

Production launcher artifacts are published only by the protected central
Ligand-X release workflow. A local Windows build is useful for diagnosing a CI
failure or checking a launcher change when hosted CI minutes are unavailable,
but it is not a release artifact and must not be uploaded to a GitHub release or
used to move the `latest` pointer.

The public launcher's frontend is pre-embedded (`frontend-public/` plus the
generated `wailsjs/` bindings). Wails uses a pure-Go WebView2 loader on Windows,
so Linux can produce a portable diagnostic executable without CGO.

## Build a portable diagnostic executable

```bash
# Go 1.22 or newer; go.mod may select a newer toolchain automatically.
export GOPATH=/tmp/ligandx-launcher-gopath
export GOCACHE=/tmp/ligandx-launcher-gocache

cd ligand-x-launcher

# Embed the same manifest, icon and version resources used by the release build.
go install github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest
goversioninfo -64 -o resource_windows_amd64.syso versioninfo.json

# Build the public production implementation as a Windows GUI executable.
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags "public production" \
  -ldflags="-H windowsgui -s -w" -o ligandx-windows-amd64.exe .

# Avoid double-embedding resources in a later Wails build.
rm -f resource_windows_amd64.syso
```

Treat `ligandx-windows-amd64.exe` as an untrusted local test output. Record its
commit, inspect it as a `PE32+` GUI executable, and smoke-test it on Windows. Do
not distribute it: the central workflow builds the canonical artifacts, records
their checksums in the signed runtime manifest, and applies the release channel's
signing policy.

## If central release CI fails

Fix the failing workflow or source change and rerun the exact central workflow
run. The launcher workflow may create an immutable versioned release, but it
deliberately cannot mark a GitHub release as latest; the central workflow does
that only after all product repositories and release artifacts validate.

Never work around a failure by:

- replacing an asset in an existing immutable release;
- uploading a local build directly to `latest`;
- dispatching the launcher publisher as an independent product release; or
- pushing core or Pro images outside the central release workflow.

If immutable artifacts for a version are already inconsistent, issue a higher
version rather than overwriting them.

## Runtime version contract

The runtime bundle contains concrete immutable `VERSION` and `PRO_VERSION` pins.
The launcher's install path verifies the signed runtime manifest before using
the bundle, and generates `INTERNAL_WORKER_SECRET` locally. Build planning and
production publication belong to the unified release tooling in the private Pro
repository; this public repository only builds and attests the launcher artifacts
requested by that workflow.
