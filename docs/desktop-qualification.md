# Desktop accessibility probe

The Linux probe runs an unmodified public launcher binary or AppImage in a fresh
Xvfb display, D-Bus session and XDG sandbox. It binds the accessibility application
to the launched process or its descendants, waits for rendered WebKit content,
and checks explicitly named controls. It never clicks, types or installs a runtime.

A passing first-run probe is not an install, upgrade or scientific qualification.
Those journeys still need a separate candidate check and runtime assertions.

## Requirements

Use the distro Python (`/usr/bin/python3`) with Linux pidfd APIs and GI AT-SPI.
The host also needs Xvfb/xauth, dbus-run-session, WebKitGTK 4.1 and, for AppImages,
squashfs-tools. On Ubuntu, the corresponding packages include `python3-gi`,
`gir1.2-atspi-2.0`, `xvfb`, `xauth`, `dbus-x11` and `squashfs-tools`.

Check the prerequisites without launching the artifact:

```sh
/usr/bin/python3 scripts/desktop_qualification.py \
  --artifact ./ligandx-linux-amd64.AppImage --check-prerequisites
```

## Run

```sh
/usr/bin/python3 scripts/desktop_qualification.py \
  --artifact ./ligandx-linux-amd64.AppImage \
  --sha256 EXPECTED_ASSET_SHA256 \
  --expect 'Set up runtime files' \
  --expect 'Download runtime' \
  --expect 'Choose version' \
  --timeout 150 --out /tmp/desktop-probe.json
```

For a locally built public binary, `make build-public` produces `build/bin/ligandx`,
the default artifact path. Use `--artifact` for any other location.

The JSON envelope records the artifact digest, PID-bound application, observed
controls, isolation and verdict. A digest mismatch, missing control, shell-only
accessibility tree, process failure or deadline produces a nonzero exit. Exit 2
identifies missing prerequisites or invalid arguments. The envelope always states
that install/upgrade passes and control activation were not performed.

Optional flags include `--python`, `--sandbox`, `--keep-sandbox`, `--max-nodes` and
`--max-depth`. `--keep-sandbox` retains the diagnostic logs and raw tree; a supplied
sandbox must be dedicated to this probe. Run `--help` for current defaults.

## Isolation and cleanup

The probe strips inherited display/session-bus and launcher override variables.
It supplies fresh XDG directories and enables AT-SPI on the isolated bus. It does
not modify the binary, its signing key, production trust roots or the owner's
installed runtime.

Window-shell nodes are not treated as a rendered first-run screen. Rendered
content must settle, and every `--expect` name must be reachable. Node/depth limits
and AT-SPI call timeouts bound observation.

The supervisor records descendant start ticks and process groups while the child
is live. Cleanup uses Linux pidfds to signal only the same observed processes,
including descendants in separate sessions. PID reuse cannot redirect a signal to
an unrelated process. Cleanup also runs when the supervisor's normal control flow
is interrupted. Descendants spawned outside an observed process tree are not
claimed as observed; this is a test harness, not a hostile-process sandbox.

## Tests and remaining release work

```sh
/usr/bin/python3 -m unittest discover -s scripts -p test_desktop_qualification.py
```

The suite covers isolation, failure verdicts, delayed WebKit rendering, role-name
normalization, PID mismatch/reuse, and real detached-child timeout cleanup. It does
not need a display or the GUI libraries for its fake AT-SPI cases. `make test` and
the required quality workflow run these regressions.

Release integration still needs real fresh-install and previous-release upgrade
journeys through the shipped UI, runtime health/data assertions, and evidence
bound to the launcher artifact digest. This probe provides the first-run
reachability boundary only.
