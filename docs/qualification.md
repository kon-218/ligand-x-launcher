# Linux installer qualification adapter

Build the headless adapter explicitly:

```bash
go build -tags qualification -o /tmp/ligandx-runtime-qualification ./cmd/runtime-qualification
go test -tags qualification ./cmd/runtime-qualification ./internal/launcher
```

Normal desktop builds exclude this command and adapter. A `public` build refuses
to construct the adapter even if someone also adds the `qualification` tag.
Production release-index selection and semantic-version downgrade policy remain
the desktop installer's authority.

The adapter reads one JSON request from stdin and writes one JSON result to
stdout. Operations are `install`, `configure`, `plan`, `start`, and `status`. Run one
adapter instance per process: the existing runtime verification policy uses
process-wide build settings. The qualification controller owns the process
deadline, image checks, endpoint/result assertions and cleanup.

Example fresh installation request (paths and public key are supplied by the
controller):

```json
{
  "action": "install",
  "config": {
    "runtime_dir": "/tmp/owned-run/lx-qualification-example",
    "project": "lx-qualification-example",
    "owner": "BUILD-03-example",
    "public_key_b64": "<trusted qualification bundle public key>",
    "launcher_version": "v1.0.0",
    "cpu_only": false
  },
  "bundle_path": "/tmp/verified-inputs/ligand-x-runtime.zip",
  "version": "candidate-c-0123456789abcdef"
}
```

The signed `ligand-x-runtime-manifest.json` and
`ligand-x-runtime-manifest.sig` companion files must be next to the archive.
Inputs are bounded, signature-checked, copied into private staging and hashed
before calling the real installer's verification, extraction and activation
methods. An empty owned root permits a fresh install through the normal guard.
The adapter refuses a nonempty root without its matching owner marker,
symlinked roots, and production Compose namespaces.

`configure` accepts `env_path`, `license_path` and `groups`; environment and
license contents are never included in the result. `start` accepts `groups`
and calls the real production service-start method. Unknown or locked groups
fail before startup instead of silently becoming a smaller scenario. `plan`
accepts the same `groups` and changes nothing: it returns `requested_services`
and `requested_images`, the images the launcher's group pull would fetch for the
installed version. Starts use `--pull=never`, so a controller qualifying an
installed predecessor must stage those images itself and verify each product
image against the predecessor's signed digests before `start`. Test
projects have isolated launcher settings, logs, Compose names and infrastructure
container names. Never use the adapter to uninstall a desktop installation.
Cleanup must check the controller's owner marker and exact Compose project.

## Stable-to-candidate upgrade authorization

A candidate ID deliberately has no semantic release ordering. The owner-approved
qualification transition therefore requires explicit signed authorization for
one candidate and one installed predecessor. No synthetic release number is
used to pass the downgrade guard.

An `install` request may add `upgrade` paths:

```json
{
  "plan": "/tmp/verified-inputs/plan.json",
  "authorization": "/tmp/verified-inputs/transition.json",
  "signature": "/tmp/verified-inputs/transition.sig",
  "predecessor_manifest": "/tmp/stable/ligand-x-runtime-manifest.json",
  "predecessor_signature": "/tmp/stable/ligand-x-runtime-manifest.sig"
}
```

Place that object under the request's `upgrade` key. Config must additionally
provide `authorization_public_key_b64` (the qualification controller's key) and
`predecessor_public_key_b64` (the trusted predecessor release key).

The controller first verifies the candidate ID with the authoritative Python
candidate algorithm. It signs the exact raw authorization JSON with Ed25519;
the detached signature is standard base64. Schema
`ligandx-qualification-transition/1` requires:

- `candidate_id`, `plan_sha256` (hash of the exact plan bytes);
- `predecessor_version`, `predecessor_manifest_sha256`;
- `target_bundle_sha256`;
- `project`, `owner`, `issued_at`, `expires_at` (RFC 3339).

Validity may span at most 30 minutes. The adapter checks the signature, times,
owner, project, plan identity and baseline, installed predecessor marker, and
both signed runtime manifests. Only then does it authorize the real installer's
non-semver transition. Replaying it after candidate activation fails the
predecessor check. Qualification signatures confer no public publication or
release-promotion authority.

## What a result establishes

The adapter result reports backend execution, installed identity, event counts
and elapsed time. It does **not** assert healthy services, intact application
data or scientific correctness by itself. The controller must independently
check these against immutable candidate artifacts. Upgrade qualification must
seed and read real predecessor projects/results and verify credentials,
settings, selections and artifacts survive activation and service restart.

`cpu_only` omits the GPU Compose overlay and disables the CUDA requirement in
that test runtime. On a GPU host this tests containers without GPU allocation;
it does not establish physical CPU-host GPU discovery behavior. Record the
actual host and container device observations. Native desktop UI, Windows and
macOS qualification require separate receipts.
