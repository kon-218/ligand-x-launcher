# Desktop qualification — AT-SPI driver for the shipped binary

This document describes the AT-SPI-based qualification tooling for the unmodified
Ligand-X desktop launcher binary (`public,webkit2_41` build tags).

## Purpose

The existing `candidate-validate` pipeline never exercises the desktop binary users
download. The 0.1.4 install/upgrade harness drove `wails dev` in Chromium with
Playwright, which tests a different binary than the shipped WebKitGTK build.

This tool drives the **real desktop binary** through its accessibility tree (AT-SPI),
not through WebDriver or browser automation. WebKitGTK exposes DOM roles and names
over AT-SPI, so a driver can find buttons like "Download runtime", "Update now", and
"Start services" by accessible name, press them, and read text — all against the
unmodified desktop build.

## What this is and is not

**This is:**
- A reusable Python script that launches the desktop binary under Xvfb
- An AT-SPI driver using `gi.repository.Atspi` to probe and interact with the UI
- A provenance recorder that captures the binary's SHA256
- An extensible scenario interface for declarative UI test steps
- A tool that reports what it actually observed, not what it claims succeeded

**This is not:**
- A fresh-install or upgrade pass (those require runtime verification)
- A substitute for Chromium/wails dev testing
- A binary patcher or modifier
- A WebDriver-based automation tool

## Prerequisites

| Requirement | Installation |
|---|---|
| `xvfb-run` / `Xvfb` | `sudo apt install xvfb xdotool` |
| `libwebkit2gtk-4.1-0` | Installed with the desktop binary dependencies |
| `GI Atspi` | `sudo apt install python3-gi gir1.2-atspi-2.0` |
| Desktop binary | `go build -tags public,webkit2_41 -o build/bin/ligand-x-launcher-linux-amd64` |

Check prerequisites without running:

```bash
python3 scripts/desktop_qualification.py --check-prerequisites
```

## Invocation

### Basic usage

```bash
python3 scripts/desktop_qualification.py \
  --binary build/bin/ligand-x-launcher-linux-amd64 \
  --scenario first_run_probe \
  --output /tmp/qualification-result.json
```

### Options

| Flag | Default | Description |
|---|---|---|
| `--binary PATH` | `build/bin/ligand-x-launcher-linux-amd64` | Path to the desktop binary |
| `--scenario NAME` | `first_run_probe` | Scenario to execute (`first_run_probe` or `fresh_install`) |
| `--timeout SECS` | `60.0` | Global timeout for the run |
| `--display DISPLAY` | `:99` | X display to use |
| `--output PATH` | stdout | Write JSON result to this path |
| `--check-prerequisites` | — | Only check prerequisites and exit |

### Scenarios

**`first_run_probe`** (default): Probes the accessibility tree without claiming the
install succeeded. Dumps the tree and checks for expected elements.

**`fresh_install`**: Drives the setup flow (download runtime, create account, select
edition, choose services). Does **not** claim the install succeeded; the caller must
verify service health independently.

## Output format

The script outputs JSON with:

```json
{
  "provenance": {
    "path": "/absolute/path/to/binary",
    "sha256": "hex-digest",
    "size_bytes": 12345678,
    "mtime": 1234567890.123
  },
  "accessibility_tree": {
    "role": "application",
    "name": "Ligand-X",
    "children": [...]
  },
  "scenario_results": [
    {
      "step": {"action": "click", "target": "Download runtime"},
      "success": true,
      "error": null,
      "duration_secs": 0.5,
      "observations": {"clicked": "Download runtime"}
    }
  ],
  "timestamp": 1234567890.123
}
```

## XDG isolation

The driver creates isolated XDG directories for each run:

- `XDG_CONFIG_HOME` — launcher configuration
- `XDG_STATE_HOME` — launcher state
- `XDG_CACHE_HOME` — launcher cache

These are created in a temporary directory and cleaned up after the run. The real
`~/.config/ligand-x-launcher` and installed `ligand-x` project are never touched.

## Extending scenarios

Scenarios are declarative sequences of steps. Define a new scenario by chaining
`add_step()` calls:

```python
from desktop_qualification import Scenario

my_scenario = (
    Scenario("my_test", "Custom test scenario")
    .add_step("wait_for_window", target="Ligand-X", timeout_secs=15.0)
    .add_step("click", target="Settings", timeout_secs=10.0)
    .add_step("wait_for_window", target="Settings", timeout_secs=10.0)
    .add_step("type", target="ORCA installation folder", value="/opt/orca")
    .add_step("click", target="Save changes", timeout_secs=10.0)
)
```

### Available step actions

| Action | Target | Value | Description |
|---|---|---|---|
| `wait_for_window` | Window title substring | — | Wait for a window with matching title |
| `click` | Accessible name | — | Click a button or control by name |
| `type` | Accessible name | Text to type | Type into a text input by name |
| `dump_tree` | — | — | Dump the current accessibility tree |

## Unit tests

Run the unit tests (no display required):

```bash
/usr/bin/python3 -m unittest discover -s scripts -p test_desktop_qualification.py -v
```

Tests use fake AT-SPI and process interfaces to prove timeout, failure, and
provenance semantics without requiring Xvfb or the desktop binary.

## Integration with candidate validation

This tool is a building block for B-253 (desktop qualification). Full integration
requires:

1. **Runtime verification**: The tool drives the UI but does not verify that services
   start correctly. The candidate validation pipeline must independently check
   container health, API responses, and job execution.

2. **Trust root injection**: The launcher's existing qualification inputs
   (`internal/launcher/qualification.go`) provide test endpoints and trust roots.
   These must be injected at runtime, not by rebuilding the binary.

3. **Upgrade scenario**: Testing upgrade from a previous release requires seeding
   the isolated XDG state with a prior installation, then running the upgrade flow.

4. **Digest binding**: BUILD-06 binds the check to the launcher binary digest so it
   carries when the launcher is unchanged. The provenance record in the output JSON
   provides this binding.

## Limitations

- **No headless display in this environment**: Xvfb is absent. The tool checks for
  this prerequisite and fails with an actionable error message.

- **AT-SPI tree depth**: WebKitGTK's accessibility tree may not expose all DOM
  elements. The driver searches by accessible name and role, but some controls may
  require alternative identification strategies.

- **Timing sensitivity**: UI transitions take time. The driver uses bounded timeouts
  and polling, but slow hosts may need adjusted timeout values.

- **No screenshot capture**: This version does not capture screenshots. The 0.1.4
  harness used Playwright's screenshot capability; AT-SPI does not provide this.
  External tools like `import` (ImageMagick) or `xdotool` + `xwd` can capture the
  Xvfb display if needed.

- **Scenario results are not qualification**: A successful scenario execution proves
  the UI responded as expected, not that the underlying operations succeeded. The
  caller must verify service health, data integrity, and scientific correctness
  independently.

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
