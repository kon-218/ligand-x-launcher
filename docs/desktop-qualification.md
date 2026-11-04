# Desktop qualification — AT-SPI probe for the shipped binary (B-253)

This document describes the AT-SPI-based qualification tooling for the unmodified
Ligand-X desktop launcher binary (`public,webkit2_41` build tags).

## Purpose

The existing `candidate-validate` pipeline never exercises the desktop binary users
download. The 0.1.4 install/upgrade harness drove `wails dev` in Chromium with
Playwright, which tests a different binary than the shipped WebKitGTK build.

This tool drives the **real desktop binary** through its accessibility tree (AT-SPI),
not through WebDriver or browser automation. WebKitGTK exposes DOM roles and names
over AT-SPI, so a probe can find buttons like "Download runtime", "Choose version",
and "Set up runtime files" by accessible name — all against the unmodified desktop
build.

## What this is and is not

**This is:**
- A Python script that launches the desktop binary under Xvfb + D-Bus isolation
- An AT-SPI reader using `gi.repository.Atspi` to probe the UI accessibility tree
- A provenance recorder that captures the binary's SHA256
- A tool that reports what it actually observed, not what it claims succeeded
- A first-run accessibility reachability probe (B-253)

**This is not:**
- A fresh-install or upgrade pass (those require runtime verification)
- A substitute for Chromium/wails dev testing
- A binary patcher or modifier
- A WebDriver-based automation tool
- A control activator (the probe never clicks or types)

## Prerequisites

| Requirement | Installation |
|---|---|
| `xvfb-run` / `Xvfb` / `xauth` | `sudo apt install xvfb xauth` |
| `dbus-run-session` | `sudo apt install dbus-x11` |
| `libwebkit2gtk-4.1-0` | Installed with the desktop binary dependencies |
| `GI Atspi` | `sudo apt install python3-gi gir1.2-atspi-2.0` |
| `unsquashfs` (for AppImage) | `sudo apt install squashfs-tools` |
| Desktop binary or AppImage | `go build -tags public,webkit2_41 -o build/bin/ligand-x-launcher-linux-amd64` |

Check prerequisites without running:

```bash
/usr/bin/python3 scripts/desktop_qualification.py --check-prerequisites \
  --artifact build/bin/ligand-x-launcher-linux-amd64
```

## Invocation

### Basic usage

```bash
/usr/bin/python3 scripts/desktop_qualification.py \
  --artifact build/bin/ligand-x-launcher-linux-amd64 \
  --expect "Download runtime" \
  --expect "Choose version" \
  --out /tmp/qualification-result.json \
  --sandbox /tmp/probe-sandbox \
  --timeout 150
```

### Probing a published AppImage

```bash
/usr/bin/python3 scripts/desktop_qualification.py \
  --artifact docs/work/evidence/efficiency-lead-20261103/ligandx-linux-amd64.AppImage \
  --sha256 af0d2fca84af0b35d0cfe6c614e3c6a640bfba042d1a1497f1ece928fc557efb \
  --expect "Set up runtime files" \
  --expect "Download runtime" \
  --expect "Choose version" \
  --out /tmp/appimage-probe.json \
  --timeout 150
```

### Options

| Flag | Default | Description |
|---|---|---|
| `--artifact PATH` | `build/bin/ligand-x-launcher-linux-amd64` | Path to the desktop binary or AppImage |
| `--timeout SECS` | `150.0` | End-to-end deadline for the isolated probe |
| `--expect NAME` | (none) | Accessible name that must be reachable (repeatable) |
| `--out PATH` | stdout | Write JSON envelope to this path |
| `--sha256 HEX` | (none) | Refuse to run unless the artifact digest matches |
| `--python PATH` | `sys.executable` | Interpreter for the isolated child (use `/usr/bin/python3`) |
| `--sandbox PATH` | (temp dir) | Use this XDG sandbox instead of creating a new one |
| `--max-nodes N` | `400` | Maximum accessibility nodes to read |
| `--max-depth N` | `40` | Maximum tree depth to traverse |
| `--keep-sandbox` | false | Keep the sandbox after the probe (holds launcher.log and result.json) |
| `--check-prerequisites` | false | Verify the host can run a probe, then exit |

## How it works

The probe runs in two stages:

1. **Supervisor stage** (outer process): Creates an isolated XDG sandbox, verifies
   prerequisites and artifact provenance, then launches the child under
   `xvfb-run -a dbus-run-session` with a fresh display and session bus. The supervisor
   enforces one end-to-end deadline and always reaps the child's process group.

2. **Child stage** (inner process, `--child`): Runs inside the isolated display and
   bus. Launches the artifact, binds the AT-SPI application to the launched PID (or a
   descendant), reads a bounded accessibility tree, then terminates the launcher's
   process group.

The probe waits for **rendered WebKit content**, not just the window shell. WebKitGTK
exposes the application and frame nodes before the page's DOM is rendered over AT-SPI.
A tree of only structural roles (application, frame, filler) with no meaningful
control names is the shell, not a rendered screen. The probe keeps polling until real
controls appear or the deadline expires.

## Output format

The script outputs a JSON envelope with:

```json
{
  "kind": "desktop_first_run_accessibility_probe",
  "ok": true,
  "reasons": [],
  "provenance": {
    "path": "/absolute/path/to/AppImage",
    "sha256": "hex-digest",
    "size_bytes": 123456789,
    "kind": "appimage",
    "expected_sha256": "hex-digest",
    "sha256_matches": true
  },
  "isolation": {
    "display": "xvfb-run -a (auto-selected server, never the owner's :1)",
    "session_bus": "dbus-run-session (fresh, never the owner's)",
    "xdg_sandbox": "/tmp/probe-sandbox",
    "stripped": ["DISPLAY", "DBUS_SESSION_BUS_ADDRESS", "..."]
  },
  "application": {
    "name": "ligandx-linux-amd64.AppImage",
    "process_id": 12345,
    "toolkit": "gtk"
  },
  "node_count": 42,
  "expected_controls": [
    {"name": "Download runtime", "reachable": true, "roles": ["push button"]},
    {"name": "Choose version", "reachable": true, "roles": ["push button"]}
  ],
  "clickable": [
    {"role": "push button", "name": "Download runtime", "depth": 3, "...": "..."}
  ],
  "claims": {
    "fresh_install_pass": false,
    "upgrade_pass": false,
    "control_activated": false,
    "meaning": "accessibility reachability of the shipped artifact's first run only"
  }
}
```

Exit codes:
- `0` = PID-bound non-empty tree and every `--expect` control reachable
- `1` = artifact died, no PID-bound application, empty or missing tree, deadline
  expired, Xvfb/bus failure, digest mismatch
- `2` = missing prerequisite or usage error

## XDG isolation

The probe creates isolated XDG directories for each run:

- `XDG_CONFIG_HOME` — launcher configuration
- `XDG_STATE_HOME` — launcher state
- `XDG_CACHE_HOME` — launcher cache
- `XDG_DATA_HOME` — launcher data

These are created in a temporary sandbox directory and cleaned up after the run
(unless `--keep-sandbox` is passed). The real `~/.config/ligand-x-launcher` and
installed `ligand-x` project are never touched.

The probe also strips production environment variables (`LIGANDX_PROJECT_PATH`,
`LIGANDX_RUNTIME_DIR`, etc.) and display/bus variables from the child environment to
prevent stale state from leaking into the probe.

## Role normalization

AT-SPI role names use both hyphenated and space-separated spellings across toolkits
(e.g., `"push-button"` vs `"push button"`). The probe normalizes roles to the
space-separated form used by the CLICKABLE tuple and test assertions. This ensures
compatible role spellings are recognized without weakening name/role assertions.

## Unit tests

Run the unit tests (no display required):

```bash
/usr/bin/python3 -m unittest discover -s scripts -p test_desktop_qualification.py -v
```

Tests use fake AT-SPI and process interfaces to prove timeout, failure, PID binding,
shell detection, and provenance semantics without requiring Xvfb or the desktop
binary.

## Limitations

- **No headless display in some environments**: Xvfb must be installed. The tool
  checks for this prerequisite and fails with an actionable error message.

- **AT-SPI tree depth**: WebKitGTK's accessibility tree may not expose all DOM
  elements. The probe searches by accessible name and role, but some controls may
  require alternative identification strategies.

- **Timing sensitivity**: WebKitGTK renders the accessibility tree lazily. The probe
  waits for rendered content within the deadline, but slow hosts may need adjusted
  `--timeout` values.

- **No screenshot capture**: This version does not capture screenshots. The 0.1.4
  harness used Playwright's screenshot capability; AT-SPI does not provide this.
  External tools like `import` (ImageMagick) or `xdotool` + `xwd` can capture the
  Xvfb display if needed.

- **Probe results are not qualification**: A successful probe proves the UI's
  accessibility tree exposed the expected controls, not that the underlying operations
  succeeded. The caller must verify service health, data integrity, and scientific
  correctness independently.

- **No control activation**: The probe never clicks, types, or activates controls. It
  only observes reachability. Control activation primitives exist (`activate()`,
  `set_text()`) but are not called by the first-run probe.

## Remaining runtime integration

This lane delivers source tooling. Completion of B-253 requires:

1. **Actual candidate fresh-install + upgrade**: Run the tool against a real
   candidate binary with Xvfb installed, verify the UI flow completes, then verify
   services start and jobs execute.

2. **Trust root and endpoint injection**: Use the launcher's qualification inputs to
   inject test endpoints without rebuilding.

3. **Integration with `candidate-validate`**: Wire this tool into the candidate
   validation pipeline as a desktop qualification step.

4. **Provenance binding**: Ensure the binary digest is recorded and checked against
   the candidate manifest.

## References

- B-253: Desktop qualification task
- BUILD-06: Carry binding for launcher digest
- [0.1.4 ad-hoc harness](../../docs/work/evidence/REL-05/launcher-harness-c-c79a5948f4a2aa4c/)
- [Qualification policy](../../docs/agent-guides/qualification-policy.md)
- [Builds and licensing](../../docs/agent-guides/builds-and-licensing.md)
