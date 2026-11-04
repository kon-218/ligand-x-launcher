#!/usr/bin/env python3
"""First-run accessibility probe for the unmodified shipped Ligand-X desktop artifact (B-253).

Runs the real desktop build (`public,webkit2_41`: the built binary or the published AppImage)
inside an isolated display, an isolated D-Bus session and an isolated XDG sandbox, then reads its
AT-SPI tree. It never patches the artifact, never substitutes `wails dev`/Chromium and never
activates a control. Seeing a screen is not a fresh-install or upgrade pass, so the envelope
reports `fresh_install_pass: false`.

  probe (default) supervises `xvfb-run -a dbus-run-session <python> this-file --child ...` under
                  one end-to-end deadline and always reaps the child's process group.
  --child         runs inside that isolated display+bus: launches the artifact, binds the AT-SPI
                  application to the launched PID (or a descendant), reads a bounded tree, then
                  terminates the launcher's own process group.

Exit 0 = PID-bound non-empty tree and every --expect control reachable; 1 = artifact died, no
PID-bound application, empty or missing tree, deadline expired, Xvfb/bus failure, digest
mismatch; 2 = missing prerequisite or usage error. Run it with the system interpreter
(/usr/bin/python3), which carries gi and Atspi.
"""

from __future__ import annotations

import argparse
import ctypes.util
import hashlib
import json
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Optional, Sequence

OK, FAILED, PREREQ = 0, 1, 2
# A stale shell must not silently retarget or pre-seed the probe.
PRODUCTION_OVERRIDES = ("LIGANDX_PROJECT_PATH", "LIGANDX_RUNTIME_DIR",
                        "LIGANDX_LAUNCHER_CONFIG_DIR", "LIGANDX_RUNTIME_BUNDLE_URL",
                        "LIGANDX_PRO_SRC_PATH", "LIGANDX_REGISTRY_TOKEN_URL",
                        "LIGANDX_VENDOR_ACCESS_TOKEN")
BUS_KEYS = ("DISPLAY", "DBUS_SESSION_BUS_ADDRESS", "AT_SPI_BUS", "NO_AT_BRIDGE")
XDG_KEYS = ("XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME")
CLICKABLE = ("push button", "toggle button", "radio button", "check box", "link", "entry",
             "text", "combo box", "slider")
# AT-SPI role names use both hyphenated and space-separated spellings across toolkits; normalize
# to the space-separated form the CLICKABLE tuple and tests expect.
ROLE_CANONICAL = {"push-button": "push button", "toggle-button": "toggle button",
                  "radio-button": "radio button", "check-box": "check box",
                  "combo-box": "combo box"}


def normalize_role(role: str) -> str:
    return ROLE_CANONICAL.get(role, role)


class ProbeFailure(Exception):
    """The probe ran but did not observe what it was asked to observe."""


class PrerequisiteMissing(Exception):
    def __init__(self, missing: Sequence[str]):
        super().__init__("missing prerequisites: " + "; ".join(missing))
        self.missing = list(missing)


@dataclass(frozen=True)
class Budget:
    max_nodes: int = 400
    max_depth: int = 40


@dataclass(frozen=True)
class ProbeConfig:
    artifact: Path
    timeout: float = 150.0
    expect: tuple[str, ...] = ()
    sandbox: Optional[Path] = None
    out: Optional[Path] = None
    python: Optional[str] = None
    expected_sha256: Optional[str] = None
    budget: Budget = Budget()
    keep_sandbox: bool = False


def tail(path: Path, limit: int = 2000) -> str:
    try:
        return path.read_text(errors="replace")[-limit:]
    except OSError:
        return ""


def system_first(name: str) -> Optional[str]:
    """Prefer the distro binary over an interpreter shim of the same name."""
    for candidate in (Path("/usr/bin") / name, Path("/bin") / name):
        if candidate.is_file() and os.access(candidate, os.X_OK):
            return str(candidate)
    return shutil.which(name)


def record_provenance(artifact: Path, expected_sha256: Optional[str] = None) -> dict[str, Any]:
    if not artifact.is_file():
        raise PrerequisiteMissing([f"no desktop artifact at {artifact}"])
    digest = hashlib.sha256()
    with artifact.open("rb") as handle:
        for block in iter(lambda: handle.read(1 << 20), b""):
            digest.update(block)
    provenance = {"path": str(artifact.resolve()), "sha256": digest.hexdigest(),
                  "size_bytes": artifact.stat().st_size,
                  "kind": "appimage" if artifact.suffix.lower() == ".appimage" else "binary"}
    if expected_sha256:
        provenance["expected_sha256"] = expected_sha256
        provenance["sha256_matches"] = provenance["sha256"] == expected_sha256
    return provenance


def gi_available(python: str, run: Callable = subprocess.run) -> bool:
    probe = "import gi; gi.require_version('Atspi', '2.0'); from gi.repository import Atspi"
    try:
        return run([python, "-c", probe], capture_output=True, timeout=30).returncode == 0
    except (OSError, subprocess.SubprocessError):
        return False


def missing_prerequisites(artifact: Path, python: str,
                          find: Callable[[str], Optional[str]] = system_first,
                          has_atspi: Callable[[str], bool] = gi_available,
                          find_library: Callable[[str], Optional[str]] = ctypes.util.find_library
                          ) -> list[str]:
    tools = (("xvfb-run", "sudo apt install xvfb"), ("Xvfb", "sudo apt install xvfb"),
             ("xauth", "sudo apt install xauth (xvfb-run needs it)"),
             ("dbus-run-session", "sudo apt install dbus-x11"))
    missing = [f"{tool} is absent: {hint}" for tool, hint in tools if find(tool) is None]
    if not artifact.is_file():
        missing.append(f"no desktop artifact at {artifact}")
    elif not os.access(artifact, os.X_OK):
        missing.append(f"{artifact} is not executable: chmod +x it")
    elif artifact.suffix.lower() == ".appimage" and find("unsquashfs") is None:
        missing.append("unsquashfs is absent, needed for AppImage extract-and-run: "
                       "sudo apt install squashfs-tools")
    if not has_atspi(python):
        missing.append(f"{python} cannot import gi Atspi: use the system interpreter "
                       "(/usr/bin/python3) with python3-gi and gir1.2-atspi-2.0, or pass --python")
    if find_library("webkit2gtk-4.1") is None:
        missing.append("libwebkit2gtk-4.1 is absent: sudo apt install libwebkit2gtk-4.1-0")
    return missing


def pid_subtree(root_pid: int, proc_root: str = "/proc") -> set[int]:
    """root_pid plus every descendant, so an AppImage runtime still binds its own UI."""
    by_parent: dict[int, list[int]] = {}
    for entry in os.listdir(proc_root):
        if entry.isdigit():
            try:
                with open(os.path.join(proc_root, entry, "status")) as handle:
                    ppid = next((int(line.split()[1]) for line in handle
                                 if line.startswith("PPid:")), 0)
                by_parent.setdefault(ppid, []).append(int(entry))
            except OSError:
                continue
    seen, stack = {root_pid}, [root_pid]
    while stack:
        for child in by_parent.get(stack.pop(), []):
            if child not in seen:
                seen.add(child)
                stack.append(child)
    return seen


def bind_application(apps: Sequence[tuple[str, int, Any]], allowed: set[int]):
    """Split AT-SPI applications into the one bound to the launched PID and the rest."""
    bound = [(name, pid, ref) for name, pid, ref in apps if pid in allowed]
    others = [{"name": name, "process_id": pid} for name, pid, _ in apps if pid not in allowed]
    return (bound[0] if bound else None), others


@dataclass
class Node:
    role: str
    name: str
    depth: int
    path: str
    states: tuple[str, ...] = ()
    actions: tuple[str, ...] = ()
    ref: Any = None

    def to_dict(self) -> dict[str, Any]:
        return {"role": self.role, "name": self.name, "depth": self.depth, "path": self.path,
                "states": list(self.states), "actions": list(self.actions)}


class AtspiReader:
    """Thin wrapper over GI Atspi: a failing query is recorded, never hidden as a success."""

    interesting = ("SENSITIVE", "VISIBLE", "SHOWING", "ENABLED", "FOCUSABLE", "EDITABLE")

    def __init__(self, atspi: Any):
        self.atspi = atspi
        self.errors: list[str] = []

    def _safe(self, what: str, read: Callable, default):
        try:
            return read()
        except Exception as exc:  # dead or lazily built subtrees fail per node
            self.errors.append(f"{what}: {type(exc).__name__}: {exc}")
            return default

    def role(self, ref) -> str:
        return self._safe("role", lambda: self.atspi.role_get_name(ref.get_role()), "unknown")

    def name(self, ref) -> str:
        return self._safe("name", lambda: ref.get_name() or "", "")

    def toolkit(self, ref) -> str:
        return self._safe("toolkit", lambda: ref.get_toolkit_name() or "", "")

    def states(self, ref) -> tuple[str, ...]:
        def read():
            state_set = ref.get_state_set()
            return tuple(name.lower() for name in self.interesting
                         if state_set.contains(getattr(self.atspi.StateType, name)))
        return self._safe("states", read, ())

    def actions(self, ref) -> tuple[str, ...]:
        return self._safe("actions",
                          lambda: tuple(ref.get_action_name(i)
                                        for i in range(ref.get_n_actions())), ())

    def children(self, ref) -> list:
        def read():
            return [child for child in (ref.get_child_at_index(i)
                                        for i in range(ref.get_child_count())) if child]
        return self._safe("children", read, [])

    def applications(self) -> list[tuple[str, int, Any]]:
        """(name, pid, ref) for each application registered on the current session bus."""
        def read():
            desktop = self.atspi.get_desktop(0)
            return [(self.name(ref), int(ref.get_process_id()), ref)
                    for ref in (desktop.get_child_at_index(i)
                                for i in range(desktop.get_child_count())) if ref]
        return self._safe("applications", read, [])


def collect_tree(root, reader: AtspiReader, budget: Budget, deadline: float,
                 now: Callable[[], float] = time.monotonic):
    """Node-bounded, depth-bounded and wall-clock-bounded read of one application tree."""
    nodes: list[Node] = []
    truncated = ""
    stack = [(root, 0, "0")]
    while stack:
        ref, depth, path = stack.pop()
        if now() >= deadline:
            break
        if len(nodes) >= budget.max_nodes or depth > budget.max_depth:
            truncated = truncated or (f"max_nodes={budget.max_nodes}"
                                      if len(nodes) >= budget.max_nodes
                                      else f"max_depth={budget.max_depth}")
            continue
        role = normalize_role(reader.role(ref))
        # Actions are read only for controls: asking a plain node for them makes the launcher's
        # own AT-SPI provider log a critical and can fail the call.
        nodes.append(Node(role, reader.name(ref), depth, path, reader.states(ref),
                          reader.actions(ref) if role in CLICKABLE else (), ref))
        stack.extend((child, depth + 1, f"{path}.{offset}")
                     for offset, child in enumerate(reader.children(ref)))
    return nodes, truncated or ("deadline" if now() >= deadline else "")


def is_shell_only(nodes: Sequence[Node]) -> bool:
    """True when the tree is the window frame without any rendered WebKit content.

    WebKitGTK registers the application and frame nodes before the page's DOM is exposed over
    AT-SPI. A tree of only structural roles (application, frame, filler, window, dialog, panel)
    with no meaningful control names is the shell, not a rendered screen. A node with a clickable
    role or a non-structural name indicates rendered content.
    """
    structural = {"application", "frame", "filler", "window", "dialog", "panel", "document web",
                  "document frame"}
    for node in nodes:
        if node.role in CLICKABLE:
            return False
        if node.role not in structural and node.role != "unknown" and node.name.strip():
            return False
    named_controls = [node for node in nodes
                     if node.name.strip() and node.role not in ("application", "frame", "filler")]
    return not named_controls


def observe(proc, reader: AtspiReader, deadline: float, budget: Budget,
            now: Callable[[], float] = time.monotonic,
            pause: Callable[[float], None] = time.sleep,
            proc_root: str = "/proc") -> dict[str, Any]:
    """Bind the launched PID, then re-read its tree until rendered content settles.

    WebKitGTK builds the page's accessibility tree lazily: the first reads after AT-SPI
    registration report only the window shell, so a single read cannot tell an empty screen from a
    screen that is not exposed yet. Two identical *content-bearing* reads can. A shell-only tree
    is never accepted as settled, even if it repeats; the probe keeps polling until real controls
    appear or the deadline expires.
    """
    last, signature, empty_bound_seen, shell_stabilized_at = None, None, False, None
    others: list[dict] = []
    while True:
        code = proc.poll()
        if code is not None:
            raise ProbeFailure(f"desktop artifact exited before its tree was readable "
                               f"(exit code {code})")
        current_time = now()
        if current_time >= deadline:
            if last is not None and not is_shell_only(
                    [Node(**{k: v for k, v in node.items()
                             if k != "ref"}) if isinstance(node, dict) else node
                     for node in (last.get("nodes") or [])]):
                return last
            if empty_bound_seen:
                raise ProbeFailure("the launched PID owns an AT-SPI application whose tree "
                                   "stayed empty (only the application node was reported)")
            if shell_stabilized_at is not None:
                raise ProbeFailure(
                    f"the accessibility tree showed only the window shell (no rendered WebKit "
                    f"controls) from t={shell_stabilized_at:.1f}s until the deadline at "
                    f"t={current_time:.1f}s; the shell is not a first-run screen")
            raise ProbeFailure(f"no AT-SPI application bound to PID {proc.pid} before the "
                               f"deadline; other applications on the bus: {others}")
        bound, others = bind_application(reader.applications(),
                                        pid_subtree(proc.pid, proc_root))
        if bound:
            name, pid, ref = bound
            nodes, truncated = collect_tree(ref, reader, budget, deadline, now)
            if len(nodes) > 1:
                shell_only = is_shell_only(nodes)
                current = tuple((node.role, node.name) for node in nodes)
                if shell_only:
                    if shell_stabilized_at is None:
                        shell_stabilized_at = current_time
                    # A shell that repeats is still a shell; do not settle on it.
                    signature = None
                last = {"application": {"name": name, "process_id": pid,
                                        "toolkit": reader.toolkit(ref)},
                        "nodes": [node.to_dict() for node in nodes],
                        "clickable": [node.to_dict() for node in nodes
                                      if node.role in CLICKABLE],
                        "other_applications": others, "truncated": truncated,
                        "settled": not shell_only and current == signature,
                        "shell_only": shell_only}
                if not shell_only and current == signature:
                    return last
                signature = current
            else:
                empty_bound_seen = True
        pause(0.5)


def activate(node: Node, hints: Sequence[str] = ("click", "press", "activate")) -> str:
    """Perform a click-like AT-SPI action. The first-run probe never calls this."""
    if node.ref is None:
        raise ProbeFailure(f"node {node.name!r} is detached from its AT-SPI reference")
    for index, action in enumerate(node.actions):
        if str(action).lower() in hints:
            if not node.ref.do_action(index):
                raise ProbeFailure(f"do_action({action!r}) failed on {node.name!r}")
            return action
    raise ProbeFailure(f"no click-like action on {node.name!r}; actions: {list(node.actions)}")


def set_text(node: Node, text: str) -> None:
    """Write into an editable AT-SPI node. The first-run probe never calls this."""
    if node.ref is None:
        raise ProbeFailure(f"node {node.name!r} is detached from its AT-SPI reference")
    if "editable" not in node.states:
        raise ProbeFailure(f"{node.name!r} is not editable (states: {list(node.states)})")
    if not node.ref.set_text_contents(text):
        raise ProbeFailure(f"set_text_contents failed on {node.name!r}")


def terminate_group(proc, killpg: Callable = os.killpg, getpgid: Callable = os.getpgid,
                    grace: float = 5.0) -> Optional[int]:
    """SIGTERM then SIGKILL an owned process group, always waiting for it.

    A group is only signalled when it is a positive, foreign process group: kernel tasks
    report pgid 0, which would mean "the caller's own group". If the child never became a
    group leader only the child itself is signalled, so the owner's session can never be killed.
    """
    pgid = None
    try:
        candidate = getpgid(proc.pid)
        if candidate > 0 and candidate != os.getpgid(0):
            pgid = candidate
    except OSError:
        pgid = None
    for step, sig in enumerate((signal.SIGTERM, signal.SIGKILL)):
        if pgid is not None:
            try:
                killpg(pgid, sig)
            except OSError:
                pass
        elif step == 0:
            proc.terminate()
        else:
            proc.kill()
        try:
            return proc.wait(timeout=grace)
        except subprocess.TimeoutExpired:
            continue
    return proc.poll()


def sandbox_environment(sandbox: Path, strip_bus: bool,
                        base: Optional[dict[str, str]] = None) -> dict[str, str]:
    """Isolated XDG roots for the probe; the child also drops any inherited display and bus."""
    keys = [*XDG_KEYS, *PRODUCTION_OVERRIDES] + (list(BUS_KEYS) if strip_bus else ["NO_AT_BRIDGE"])
    env = {key: value for key, value in (os.environ if base is None else base).items()
           if key not in keys}
    env.update({f"XDG_{name.upper()}_HOME": str(sandbox / name)
                for name in ("config", "state", "cache", "data")})
    env.update({"GTK_A11Y": "atspi", "APPIMAGE_EXTRACT_AND_RUN": "1"})  # never a patched artifact
    return env


def enable_accessibility(run: Callable = subprocess.run, timeout: float = 20.0) -> str:
    """Ask the isolated AT-SPI bus to register its registry; touches nothing outside the bus."""
    argv = ["gdbus", "call", "--session", "--dest", "org.a11y.Bus",
            "--object-path", "/org/a11y/bus", "--method",
            "org.freedesktop.DBus.Properties.Set", "org.a11y.Status", "IsEnabled", "<true>"]
    try:
        result = run(argv, capture_output=True, text=True, timeout=timeout)
    except (OSError, subprocess.SubprocessError) as exc:
        return f"failed to set org.a11y.Status.IsEnabled: {exc}"
    return "org.a11y.Status.IsEnabled=true" if result.returncode == 0 else (
        f"failed to set org.a11y.Status.IsEnabled (rc={result.returncode}): "
        f"{(result.stderr or result.stdout or '').strip()[:200]}")


def launch_artifact(artifact: Path, sandbox: Path, log_path: Path, env: dict[str, str],
                    popen: Callable = subprocess.Popen):
    # Files, not pipes: an undrained pipe would block the probe on a chatty launcher.
    log_path.parent.mkdir(parents=True, exist_ok=True)
    (sandbox / "run").mkdir(parents=True, exist_ok=True)
    with open(log_path, "wb") as handle:
        return popen([str(artifact)], cwd=str(sandbox / "run"), env=env,
                     stdout=handle, stderr=handle, start_new_session=True)


def real_reader() -> AtspiReader:
    import gi

    gi.require_version("Atspi", "2.0")
    from gi.repository import Atspi

    Atspi.set_timeout(5000, 5000)  # bounds each AT-SPI call, not only our own traversal
    Atspi.init()
    return AtspiReader(Atspi)


def probe_child(artifact: Path, sandbox: Path, out: Path, timeout: float, budget: Budget,
                launch: Callable = launch_artifact,
                enable: Callable[[], str] = enable_accessibility,
                reader_factory: Callable[[], AtspiReader] = real_reader,
                now: Callable[[], float] = time.monotonic,
                pause: Callable[[float], None] = time.sleep,
                reap: Callable = terminate_group,
                log_path: Optional[Path] = None) -> int:
    """Run one first-run probe inside an already-isolated display and session bus."""
    log_path = log_path or (sandbox / "launcher.log")
    payload: dict[str, Any] = {"role": "child", "artifact": str(artifact),
                               "deadline_seconds": timeout}
    proc = reader = None
    code = FAILED
    try:
        payload["accessibility_bus"] = enable()
        proc = launch(artifact, sandbox, log_path, sandbox_environment(sandbox, False))
        reader = reader_factory()
        tree = observe(proc, reader, now() + timeout, budget, now=now, pause=pause)
        payload.update(tree)
        if tree["settled"]:
            code = OK
        else:
            payload["failure"] = ("the accessibility tree was still changing when the deadline "
                                  "expired, so the first-run screen was never observed settled")
    except ProbeFailure as exc:
        payload["failure"] = str(exc)
    except Exception as exc:  # a GI failure must be reported, never counted as success
        payload["error"] = f"{type(exc).__name__}: {exc}"
    finally:
        if proc is not None:
            payload["cleanup_exit_code"] = reap(proc)
        payload["launcher_log_tail"] = tail(log_path)
        payload["reader_errors"] = reader.errors if reader else []
        out.parent.mkdir(parents=True, exist_ok=True)
        out.write_text(json.dumps(payload, indent=2))
    return code


def child_command(cfg: ProbeConfig, sandbox: Path, result_path: Path, python: str) -> list[str]:
    return [system_first("xvfb-run") or "xvfb-run", "-a", "-s", "-screen 0 1280x800x24",
            system_first("dbus-run-session") or "dbus-run-session", python,
            str(Path(__file__).resolve()), "--child", "--artifact", str(cfg.artifact),
            "--sandbox", str(sandbox), "--out", str(result_path),
            "--timeout", str(max(cfg.timeout - 30.0, 10.0)),
            "--max-nodes", str(cfg.budget.max_nodes), "--max-depth", str(cfg.budget.max_depth)]


def supervise(cmd: Sequence[str], env: dict[str, str], log_path: Path, timeout: float,
              popen: Callable = subprocess.Popen, now: Callable[[], float] = time.monotonic,
              pause: Callable[[float], None] = time.sleep) -> tuple[int, bool]:
    """Run the isolated child under one end-to-end deadline and always reap its group."""
    with open(log_path, "wb") as handle:
        proc = popen(list(cmd), stdout=handle, stderr=subprocess.STDOUT, env=env,
                     start_new_session=True)
        deadline, timed_out = now() + timeout, False
        while proc.poll() is None:
            if now() >= deadline:
                timed_out = True
                break
            pause(0.25)
        if timed_out or proc.poll() is None:
            terminate_group(proc)
    return (proc.returncode if proc.returncode is not None else -1), timed_out


def judge(payload: dict[str, Any], expect: Sequence[str]) -> tuple[list[str], list[dict]]:
    reasons = [str(payload[key]) for key in ("failure", "error") if payload.get(key)]
    nodes = payload.get("nodes") or []
    if not nodes:
        reasons.append("the child returned no accessibility tree")
    verdicts = []
    for wanted in expect:
        hits = [node for node in nodes
                if str(node.get("name", "")).strip().lower() == wanted.strip().lower()]
        verdicts.append({"name": wanted, "reachable": bool(hits),
                         "roles": sorted({str(node["role"]) for node in hits})})
        if not hits:
            reasons.append(f"expected control is not reachable by accessible name: {wanted!r}")
    return reasons, verdicts


def run_probe(cfg: ProbeConfig, supervisor: Optional[Callable] = None,
              read_result: Callable[[Path], Any] = lambda path: json.loads(path.read_text())
              ) -> dict[str, Any]:
    """Supervise one isolated probe and assemble the evidence envelope."""
    supervise_child = supervisor or supervise
    missing = missing_prerequisites(cfg.artifact, cfg.python or sys.executable)
    if missing:
        raise PrerequisiteMissing(missing)
    provenance = record_provenance(cfg.artifact, cfg.expected_sha256)
    if provenance.get("sha256_matches") is False:
        raise ProbeFailure(f"artifact sha256 {provenance['sha256']} does not match the expected "
                           f"{cfg.expected_sha256}; refusing to launch the wrong artifact")
    sandbox = Path(cfg.sandbox or tempfile.mkdtemp(prefix="ligandx-desktop-probe-"))
    for name in ("config", "state", "cache", "data", "run"):
        (sandbox / name).mkdir(parents=True, exist_ok=True)
    child_log, result_path = sandbox / "child.log", sandbox / "result.json"
    code, timed_out = supervise_child(child_command(cfg, sandbox, result_path,
                                                    cfg.python or sys.executable),
                                      sandbox_environment(sandbox, True), child_log, cfg.timeout)
    if result_path.is_file():
        try:
            payload = read_result(result_path)
        except (OSError, ValueError) as exc:
            payload = {"error": f"the child result was unreadable: {exc}"}
    else:
        payload = {"error": "the isolated child wrote no result; it died before probing"}
    if timed_out:
        payload.setdefault("failure", f"the child did not finish within {cfg.timeout}s and its "
                                      "process group was terminated")
    reasons, verdicts = judge(payload, cfg.expect)
    if code != 0:
        reasons.append(f"the isolated probe exited with code {code}")
    return {
        "kind": "desktop_first_run_accessibility_probe",
        "ok": not reasons and code == 0, "reasons": reasons, "provenance": provenance,
        "isolation": {"display": "xvfb-run -a (auto-selected server, never the owner's :1)",
                      "session_bus": "dbus-run-session (fresh, never the owner's)",
                      "xdg_sandbox": str(sandbox),
                      "stripped": sorted([*BUS_KEYS, *XDG_KEYS, *PRODUCTION_OVERRIDES])},
        "child": {"returncode": code, "timed_out": timed_out,
                  "log_tail": tail(child_log, 800)},
        "application": payload.get("application"),
        "accessibility_bus": payload.get("accessibility_bus"),
        "other_applications": payload.get("other_applications", []),
        "truncated": payload.get("truncated", ""),
        "node_count": len(payload.get("nodes") or []),
        "expected_controls": verdicts, "clickable": payload.get("clickable", []),
        "launcher_log_tail": payload.get("launcher_log_tail", ""),
        "reader_errors": payload.get("reader_errors", []),
        "claims": {"fresh_install_pass": False, "upgrade_pass": False,
                   "control_activated": False,
                   "meaning": "accessibility reachability of the shipped artifact's first run "
                              "only; install and upgrade still require the runtime gates"},
    }


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = argparse.ArgumentParser(
        description="Probe the shipped Ligand-X desktop artifact over AT-SPI (B-253).")
    parser.add_argument("--artifact", type=Path,
                        default=Path("build/bin/ligand-x-launcher-linux-amd64"),
                        help="desktop binary or published AppImage, unmodified")
    parser.add_argument("--timeout", type=float, default=150.0,
                        help="end-to-end deadline for the isolated probe, in seconds")
    parser.add_argument("--expect", action="append", default=[],
                        help="accessible name that must be reachable (repeatable)")
    parser.add_argument("--out", type=Path, help="write the JSON envelope here")
    parser.add_argument("--sha256", dest="expected_sha256",
                        help="refuse to run unless the artifact digest matches this value")
    parser.add_argument("--python", help="interpreter for the isolated child (system python3)")
    parser.add_argument("--sandbox", type=Path,
                        help="use this XDG sandbox instead of creating a new one")
    parser.add_argument("--max-nodes", type=int, default=400)
    parser.add_argument("--max-depth", type=int, default=40)
    parser.add_argument("--keep-sandbox", action="store_true",
                        help="keep the sandbox, which holds launcher.log and result.json")
    parser.add_argument("--check-prerequisites", action="store_true",
                        help="verify the host can run a probe, then exit")
    parser.add_argument("--child", action="store_true", help=argparse.SUPPRESS)
    args = parser.parse_args(argv)

    if args.child:
        if not (args.out and args.sandbox):
            parser.error("--child requires --out and --sandbox")
        return probe_child(args.artifact, args.sandbox, args.out, args.timeout,
                           Budget(args.max_nodes, args.max_depth))
    if args.check_prerequisites:
        missing = missing_prerequisites(args.artifact, args.python or sys.executable)
        for problem in missing:
            print(f"missing: {problem}", file=sys.stderr)
        print("all prerequisites present" if not missing
              else f"{len(missing)} prerequisite(s) missing", file=sys.stderr)
        return PREREQ if missing else OK

    config = ProbeConfig(args.artifact, args.timeout, tuple(args.expect), args.sandbox,
                         args.out, args.python, args.expected_sha256,
                         Budget(args.max_nodes, args.max_depth), args.keep_sandbox)
    try:
        envelope = run_probe(config)
    except PrerequisiteMissing as exc:
        print(f"error: {exc}", file=sys.stderr)
        return PREREQ
    except ProbeFailure as exc:
        print(f"probe failed: {exc}", file=sys.stderr)
        return FAILED
    text = json.dumps(envelope, indent=2)
    if config.out:
        config.out.parent.mkdir(parents=True, exist_ok=True)
        config.out.write_text(text + "\n")
        print(f"{'ok' if envelope['ok'] else 'FAILED'}: {config.out}", file=sys.stderr)
    else:
        print(text)
    if config.sandbox is None and not config.keep_sandbox:
        shutil.rmtree(envelope["isolation"]["xdg_sandbox"], ignore_errors=True)
    return OK if envelope["ok"] else FAILED


if __name__ == "__main__":
    sys.exit(main())
