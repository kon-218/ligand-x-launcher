#!/usr/bin/env python3
"""Unit regressions for the desktop first-run probe, with fakes for Atspi and processes.

Nothing here touches a display, a session bus or the real launcher. These tests pin the
semantics that make the probe trustworthy: a dead artifact, a missing or empty tree, a PID that
does not match, an expired deadline, an Xvfb/bus failure and process-group cleanup must all be
failures, and provenance must be a real digest.
"""

from __future__ import annotations

import hashlib
import json
import os
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import desktop_qualification as probe  # noqa: E402


class Clock:
    """Deterministic clock, so deadline behaviour does not depend on wall time."""

    def __init__(self, start: float = 0.0, step: float = 0.0):
        self.now = start
        self.step = step

    def __call__(self) -> float:
        value = self.now
        self.now += self.step
        return value

    def advance(self, seconds: float) -> None:
        self.now += seconds


class FakeAccessible:
    """Mirrors the slice of the GI Atspi surface that the driver actually calls."""

    def __init__(self, role="filler", name="", children=(), states=(), actions=(), pid=0,
                 toolkit="gtk", fail=()):
        self.role, self.name, self.child_list = role, name, list(children)
        self.states, self.actions = tuple(states), list(actions)
        self.pid, self.toolkit_name = pid, toolkit
        self.fail = set(fail)
        self.performed, self.texts = [], []

    def _guard(self, method):
        if method in self.fail:
            raise RuntimeError(f"simulated {method} failure")

    def get_role(self):
        self._guard("get_role")
        return self.role

    def get_name(self):
        self._guard("get_name")
        return self.name

    def get_child_count(self):
        return len(self.child_list)

    def get_child_at_index(self, index):
        return self.child_list[index]

    def get_state_set(self):
        self._guard("get_state_set")
        return FakeStateSet(self.states)

    def get_toolkit_name(self):
        return self.toolkit_name

    def get_process_id(self):
        return self.pid

    def get_n_actions(self):
        return len(self.actions)

    def get_action_name(self, index):
        return self.actions[index]

    def do_action(self, index):
        self.performed.append(index)
        return "do_action" not in self.fail

    def set_text_contents(self, text):
        self.texts.append(text)
        return "set_text_contents" not in self.fail


class FakeStateSet:
    def __init__(self, states):
        self.states = set(states)

    def contains(self, state):
        return state in self.states


class FakeAtspi:
    """Stands in for gi.repository.Atspi."""

    class StateType:
        SENSITIVE, VISIBLE, SHOWING = "sensitive", "visible", "showing"
        ENABLED, FOCUSABLE, EDITABLE = "enabled", "focusable", "editable"

    def __init__(self, apps=(), desktop_fails=False):
        self._desktop = FakeAccessible("desktop", "", children=apps)
        self.desktop_fails = desktop_fails

    def role_get_name(self, role):
        return role

    def get_desktop(self, index):
        if self.desktop_fails:
            raise RuntimeError("no accessibility bus")
        return self._desktop


class FakeProcess:
    """Popen-shaped fake. `codes` are successive poll() results; wait() mirrors an exit."""

    def __init__(self, pid=100, codes=None, exit_code=None, stuck_wait=False):
        self.pid = pid
        self.codes = [None] if codes is None else list(codes)
        self.exit_code = exit_code
        self.returncode = None
        self.stuck_wait = stuck_wait
        self.terminations = 0
        self.kills = 0
        self.wait_calls = []

    def poll(self):
        code = self.codes.pop(0) if len(self.codes) > 1 else self.codes[0]
        if code is not None:
            self.returncode = code
        return code

    def terminate(self):
        self.terminations += 1
        self.returncode = -signal.SIGTERM

    def kill(self):
        self.kills += 1
        self.returncode = -signal.SIGKILL

    def wait(self, timeout=None):
        self.wait_calls.append(timeout)
        if self.stuck_wait:
            raise subprocess.TimeoutExpired(cmd="fake", timeout=timeout)
        if self.returncode is None:
            self.returncode = self.exit_code
        return self.returncode


def write_proc_tree(root: Path, pid: int, ppid: int) -> None:
    directory = root / str(pid)
    directory.mkdir(parents=True, exist_ok=True)
    (directory / "status").write_text(f"Name:\tfake\nPid:\t{pid}\nPPid:\t{ppid}\n")


def reader_for(*apps, **kwargs) -> probe.AtspiReader:
    return probe.AtspiReader(FakeAtspi(apps, **kwargs))


def first_run_tree(pid: int = 100) -> FakeAccessible:
    """The shape the shipped binary really exposes on a fresh first run."""
    def button(name):
        return FakeAccessible("push-button", name, states=("sensitive", "visible"),
                              actions=["click"])

    frame = FakeAccessible("frame", "Ligand-X", children=[
        FakeAccessible("document web", "", children=[
            FakeAccessible("heading", "Set up runtime files"),
            button("Download runtime"), button("Choose version")])])
    return FakeAccessible("application", "ligandx-linux-amd64.AppImage", children=[frame], pid=pid)


class ProvenanceTests(unittest.TestCase):
    def test_digest_size_and_kind_are_recorded(self):
        with tempfile.TemporaryDirectory() as tmp:
            artifact = Path(tmp) / "ligandx-linux-amd64.AppImage"
            artifact.write_bytes(b"ligand-x")
            provenance = probe.record_provenance(artifact)
            self.assertEqual(provenance["sha256"], hashlib.sha256(b"ligand-x").hexdigest())
            self.assertEqual(provenance["kind"], "appimage")
            self.assertEqual(provenance["size_bytes"], 8)
            self.assertNotIn("sha256_matches", provenance)

    def test_expected_digest_match_and_mismatch_are_visible(self):
        with tempfile.TemporaryDirectory() as tmp:
            artifact = Path(tmp) / "launcher"
            artifact.write_bytes(b"\x00" * (1 << 20) + b"tail")  # forces a multi-block read
            actual = probe.record_provenance(artifact)["sha256"]
            self.assertEqual(actual, hashlib.sha256(b"\x00" * (1 << 20) + b"tail").hexdigest())
            self.assertTrue(probe.record_provenance(artifact, actual)["sha256_matches"])
            self.assertFalse(probe.record_provenance(artifact, "f" * 64)["sha256_matches"])

    def test_missing_artifact_is_a_prerequisite_failure(self):
        with self.assertRaises(probe.PrerequisiteMissing):
            probe.record_provenance(Path("/nonexistent/ligandx-launcher"))


class PrerequisiteTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.artifact = Path(self.tmp.name) / "launcher"
        self.artifact.write_bytes(b"x")
        self.artifact.chmod(0o755)

    def test_absent_xvfb_and_session_bus_are_actionable(self):
        missing = probe.missing_prerequisites(self.artifact, "/usr/bin/python3",
                                             find=lambda name: None,
                                             has_atspi=lambda python: True,
                                             find_library=lambda name: "lib.so")
        self.assertEqual(len(missing), 4)
        self.assertTrue(all("sudo apt install" in problem for problem in missing))
        self.assertTrue(any("xvfb-run" in problem for problem in missing))

    def test_interpreter_without_gi_names_the_system_interpreter(self):
        missing = probe.missing_prerequisites(self.artifact, "/bin/python3",
                                             find=lambda name: "/usr/bin/" + name,
                                             has_atspi=lambda python: False,
                                             find_library=lambda name: "lib.so")
        self.assertEqual(len(missing), 1)
        self.assertIn("/usr/bin/python3", missing[0])

    def test_appimage_needs_unsquashfs_for_extract_and_run(self):
        appimage = Path(self.tmp.name) / "ligandx.AppImage"
        appimage.write_bytes(b"x")
        appimage.chmod(0o755)
        missing = probe.missing_prerequisites(appimage, "/usr/bin/python3",
                                             find=lambda name: None if name == "unsquashfs"
                                             else "/usr/bin/" + name,
                                             has_atspi=lambda python: True,
                                             find_library=lambda name: "lib.so")
        self.assertEqual(len(missing), 1)
        self.assertIn("squashfs-tools", missing[0])

    def test_non_executable_artifact_is_reported(self):
        self.artifact.chmod(0o644)
        missing = probe.missing_prerequisites(self.artifact, "/usr/bin/python3",
                                             find=lambda name: "/usr/bin/" + name,
                                             has_atspi=lambda python: True,
                                             find_library=lambda name: "lib.so")
        self.assertEqual(len(missing), 1)
        self.assertIn("chmod +x", missing[0])

    def test_gi_available_asks_the_supplied_interpreter(self):
        run = mock.Mock(return_value=mock.Mock(returncode=0))
        self.assertTrue(probe.gi_available("/usr/bin/python3", run=run))
        self.assertEqual(run.call_args[0][0][:2], ["/usr/bin/python3", "-c"])
        self.assertFalse(probe.gi_available("/nope", run=mock.Mock(side_effect=OSError)))

    def test_system_first_resolves_a_distro_tool_and_reports_absence(self):
        self.assertIn(probe.system_first("sh"), ("/usr/bin/sh", "/bin/sh"))
        self.assertIsNone(probe.system_first("ligandx-not-installed"))


class PidBindingTests(unittest.TestCase):
    def test_descendants_of_the_launched_pid_are_allowed(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for pid, ppid in ((100, 1), (101, 100), (102, 101), (999, 1)):
                write_proc_tree(root, pid, ppid)
            self.assertEqual(probe.pid_subtree(100, str(root)), {100, 101, 102})

    def test_binding_is_by_pid_not_by_name(self):
        impostor = FakeAccessible("application", "ligandx-linux-amd64.AppImage", pid=999)
        ours = first_run_tree(pid=101)
        bound, others = probe.bind_application(
            [(impostor.get_name(), 999, impostor), (ours.get_name(), 101, ours)], {100, 101})
        self.assertIs(bound[2], ours)
        self.assertEqual(others, [{"name": ours.get_name(), "process_id": 999}])


class ObserveTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.proc_root = Path(self.tmp.name)
        write_proc_tree(self.proc_root, 100, 1)
        self.clock = Clock()
        self.kwargs = {"now": self.clock, "pause": self.clock.advance,
                       "proc_root": str(self.proc_root)}

    def observe(self, process, *apps, deadline=10.0, budget=None):
        return probe.observe(process, reader_for(*apps), deadline,
                             budget or probe.Budget(), **self.kwargs)

    def test_first_run_controls_are_reachable(self):
        result = self.observe(FakeProcess(100), first_run_tree())
        self.assertEqual({node["name"] for node in result["clickable"]},
                         {"Download runtime", "Choose version"})
        self.assertEqual(result["application"],
                         {"name": "ligandx-linux-amd64.AppImage", "process_id": 100,
                          "toolkit": "gtk"})
        self.assertEqual(result["truncated"], "")
        self.assertNotIn("failure", json.dumps(result))

    def test_a_dead_artifact_is_a_failure(self):
        with self.assertRaisesRegex(probe.ProbeFailure, "exited before its tree was readable"):
            self.observe(FakeProcess(100, codes=[3]), first_run_tree())

    def test_a_pid_mismatch_fails_and_reports_what_was_seen(self):
        impostor = FakeAccessible("application", "ligandx-linux-amd64.AppImage",
                                 children=[FakeAccessible("frame", "Ligand-X")], pid=999)
        with self.assertRaisesRegex(probe.ProbeFailure,
                                    r"no AT-SPI application bound to PID 100") as caught:
            self.observe(FakeProcess(100), impostor, deadline=3.0)
        self.assertIn("999", str(caught.exception))

    def test_a_bound_application_with_an_empty_tree_is_a_failure(self):
        with self.assertRaisesRegex(probe.ProbeFailure, "tree stayed empty"):
            self.observe(FakeProcess(100), FakeAccessible("application", "ligandx", pid=100),
                         deadline=3.0)

    def test_an_unreadable_bus_fails_without_hiding_the_error(self):
        reader = reader_for(desktop_fails=True)
        with self.assertRaisesRegex(probe.ProbeFailure, "no AT-SPI application bound"):
            probe.observe(FakeProcess(100), reader, 2.0, probe.Budget(), **self.kwargs)
        self.assertIn("no accessibility bus", " ".join(reader.errors))

    def test_the_node_budget_truncates_instead_of_running_away(self):
        wide = FakeAccessible("application", "ligandx", pid=100,
                              children=[FakeAccessible("push-button", f"n{index}", pid=100)
                                        for index in range(50)])
        result = self.observe(FakeProcess(100), wide, budget=probe.Budget(max_nodes=10))
        self.assertEqual(len(result["nodes"]), 10)
        self.assertEqual(result["truncated"], "max_nodes=10")

    def test_the_depth_budget_truncates(self):
        node = FakeAccessible("push-button", "Download runtime", pid=100)
        for _ in range(15):
            node = FakeAccessible("filler", "", children=[node], pid=100)
        app = FakeAccessible("application", "ligandx", children=[node], pid=100)
        reader = reader_for(app)
        nodes, truncated = probe.collect_tree(app, reader, probe.Budget(max_nodes=100, max_depth=10), 10.0,
                                              now=Clock(start=0.0, step=0.0))
        self.assertEqual(truncated, "max_depth=10")
        self.assertLessEqual(max(item.depth for item in nodes), 10)

    def test_traversal_stops_at_the_deadline(self):
        wide = FakeAccessible("application", "ligandx", pid=100,
                              children=[FakeAccessible("push-button", f"n{index}", pid=100)
                                        for index in range(20)])
        reader = reader_for(wide)
        clock = Clock()

        def now():
            clock.advance(0.5)
            return clock.now

        nodes, truncated = probe.collect_tree(wide, reader, probe.Budget(), 2.0, now=now)
        self.assertLess(len(nodes), 21)
        self.assertEqual(truncated, "deadline")

    def test_per_node_failures_are_recorded_and_traversal_continues(self):
        broken = FakeAccessible("push-button", "Download runtime", fail={"get_role"})
        root = FakeAccessible("application", "ligandx",
                              children=[FakeAccessible("frame", "Ligand-X", children=[broken])],
                              pid=100)
        reader = reader_for(root)
        result = probe.observe(FakeProcess(100), reader, 10.0, probe.Budget(), **self.kwargs)
        self.assertEqual(result["nodes"][-1]["role"], "unknown")
        self.assertIn("role: RuntimeError: simulated get_role failure", reader.errors[0])

    def test_a_stable_shell_followed_by_rendered_controls_settles_on_content(self):
        """WebKitGTK exposes the shell first, then fills in controls; the probe must wait."""
        shell = FakeAccessible("application", "ligandx", pid=100,
                               children=[FakeAccessible("frame", "Ligand-X", pid=100,
                                                       children=[FakeAccessible("filler", "", pid=100)])])
        rendered = first_run_tree(pid=100)
        reads = [shell, shell, rendered, rendered]
        read_index = [0]

        def fake_applications():
            idx = min(read_index[0], len(reads) - 1)
            read_index[0] += 1
            app = reads[idx]
            return [(app.get_name(), app.get_process_id(), app)]

        reader = reader_for()
        reader.applications = fake_applications
        result = probe.observe(FakeProcess(100), reader, 20.0, probe.Budget(), **self.kwargs)
        self.assertTrue(result["settled"])
        self.assertFalse(result.get("shell_only", False))
        self.assertEqual({node["name"] for node in result["clickable"]},
                         {"Download runtime", "Choose version"})

    def test_a_permanent_shell_fails_honestly_at_the_deadline(self):
        """A shell that never fills in must fail, not silently succeed."""
        shell = FakeAccessible("application", "ligandx", pid=100,
                               children=[FakeAccessible("frame", "Ligand-X", pid=100,
                                                       children=[FakeAccessible("filler", "", pid=100)])])
        clock = Clock(start=0.0, step=0.5)

        def fake_applications():
            return [(shell.get_name(), shell.get_process_id(), shell)]

        reader = reader_for()
        reader.applications = fake_applications
        with self.assertRaisesRegex(probe.ProbeFailure, "only the window shell"):
            probe.observe(FakeProcess(100), reader, 3.0, probe.Budget(),
                         now=clock, pause=clock.advance, proc_root=str(self.proc_root))


class TerminationTests(unittest.TestCase):
    def test_the_group_gets_sigterm_then_sigkill_and_is_always_waited_on(self):
        proc = FakeProcess(100, stuck_wait=True)
        sent = []
        result = probe.terminate_group(proc, killpg=lambda pgid, sig: sent.append((pgid, sig)),
                                       getpgid=lambda pid: 7, grace=0.01)
        self.assertEqual(sent, [(7, signal.SIGTERM), (7, signal.SIGKILL)])
        self.assertEqual(proc.wait_calls, [0.01, 0.01])
        self.assertIsNone(result)

    def test_sigterm_is_enough_when_the_group_exits(self):
        proc = FakeProcess(100, exit_code=0)
        sent = []
        result = probe.terminate_group(proc, killpg=lambda pgid, sig: sent.append(sig),
                                       getpgid=lambda pid: 7)
        self.assertEqual(sent, [signal.SIGTERM])
        self.assertEqual(result, 0)

    def test_the_callers_own_process_group_is_never_signalled(self):
        proc = FakeProcess(pid=os.getpid(), exit_code=-signal.SIGTERM)
        sent = []
        probe.terminate_group(proc, killpg=lambda pgid, sig: sent.append(pgid),
                              getpgid=lambda pid: os.getpgid(0))
        self.assertEqual(sent, [])
        self.assertEqual(proc.terminations, 1)

    def test_a_zero_process_group_is_never_signalled(self):
        """Kernel tasks report pgid 0, which killpg would read as the caller's own group."""
        proc = FakeProcess(100, exit_code=-signal.SIGTERM)
        sent = []
        probe.terminate_group(proc, killpg=lambda pgid, sig: sent.append(pgid),
                              getpgid=lambda pid: 0)
        self.assertEqual(sent, [])
        self.assertEqual(proc.terminations, 1)

    def test_a_refused_killpg_does_not_abort_the_reap(self):
        proc = FakeProcess(100, exit_code=0)

        def refuse(pgid, sig):
            raise PermissionError("not ours")

        self.assertEqual(probe.terminate_group(proc, killpg=refuse, getpgid=lambda pid: 7), 0)

    def test_a_child_that_is_not_a_group_leader_is_still_reaped(self):
        proc = FakeProcess(100, exit_code=0)

        def unknown(pid):
            raise ProcessLookupError("gone")

        # No group to signal, so the child itself is terminated and then waited on.
        self.assertEqual(probe.terminate_group(proc, killpg=lambda *a: None, getpgid=unknown),
                         -signal.SIGTERM)
        self.assertEqual(proc.terminations, 1)


class SuperviseTests(unittest.TestCase):
    def test_a_timeout_still_terminates_the_owned_group(self):
        clock = Clock()
        stuck = FakeProcess(100)
        with tempfile.TemporaryDirectory() as tmp:
            with mock.patch.object(probe, "terminate_group") as reaper:
                code, timed_out = probe.supervise(["true"], {}, Path(tmp) / "child.log", 2.0,
                                                  popen=lambda *a, **k: stuck, now=clock,
                                                  pause=clock.advance, subtree=lambda pid: {pid},
                                                  sweep=lambda pid, expected=None: [])
            reaper.assert_called_once_with(stuck)
        self.assertTrue(timed_out)
        self.assertEqual(code, -1)

    def test_a_child_exit_without_a_timeout_is_passed_through(self):
        clock = Clock()
        with tempfile.TemporaryDirectory() as tmp:
            with mock.patch.object(probe, "terminate_group") as reaper:
                code, timed_out = probe.supervise(["true"], {}, Path(tmp) / "child.log", 5.0,
                                                  popen=lambda *a, **k: FakeProcess(
                                                      100, codes=[3], exit_code=3),
                                                  now=clock, pause=clock.advance)
            reaper.assert_not_called()
        self.assertEqual((code, timed_out), (3, False))

    def test_a_descendant_that_survives_the_sweep_aborts_the_probe(self):
        """The supervisor must not report over an owned process it could not reap."""
        clock = Clock()
        with tempfile.TemporaryDirectory() as tmp:
            with mock.patch.object(probe, "terminate_group"):
                with self.assertRaisesRegex(probe.ProbeFailure, "survived cleanup"):
                    probe.supervise(["true"], {}, Path(tmp) / "child.log", 2.0,
                                    popen=lambda *a, **k: FakeProcess(100), now=clock,
                                    pause=clock.advance, subtree=lambda pid: {pid, 777},
                                    sweep=lambda pid, expected=None: [
                                        {"pid": 777, "pgid": 777, "survived": True}])


class RealTimeoutCleanupTests(unittest.TestCase):
    """Real processes only: faked process APIs cannot prove that nothing is orphaned."""

    # The child records its own pid, spawns a separate-session grandchild that records itself,
    # and both hang; the supervisor must reap both. Marker names pin which pids this test owns.
    GRANDCHILD_SCRIPT = ("import os,sys,time;"
                         "open(sys.argv[1],\\\"w\\\").write(str(os.getpid()));"
                         "time.sleep(600)")
    HANG = ("import os, subprocess, sys, time\n"
            "open(sys.argv[1], 'w').write(str(os.getpid()))\n"
            f"subprocess.Popen([sys.executable, '-c', \"{GRANDCHILD_SCRIPT}\", sys.argv[2]], "
            "start_new_session=True)\n"
            "time.sleep(600)\n")

    def wait_for_file(self, path: Path, limit: float = 10.0) -> bool:
        deadline = time.monotonic() + limit
        while time.monotonic() < deadline:
            if path.is_file():
                return True
            time.sleep(0.05)
        return False

    def recorded_pids(self, root: Path) -> list[int]:
        return sorted(int(entry.read_text().strip()) for entry in root.glob("owned.*")
                      if entry.read_text().strip().isdigit())

    def supervise_hanging_tree(self, root: Path, hang: str) -> tuple[int, bool]:
        cmd = [sys.executable, "-c", hang, str(root / "owned.child"),
               str(root / "owned.grandchild")]
        log_path = root / "child.log"
        with open(log_path, "wb") as log_handle:
            proc = subprocess.Popen(cmd, start_new_session=True, stdout=log_handle,
                                    stderr=subprocess.STDOUT)
            self.assertTrue(self.wait_for_file(root / "owned.grandchild"),
                            "the separate-session descendant never started")
            return probe.supervise(cmd, {}, log_path, 1.0, popen=lambda *a, **k: proc,
                                   now=time.monotonic, pause=lambda s: time.sleep(s))

    def test_a_timeout_reaps_a_separate_session_descendant(self):
        """start_new_session escapes the outer group signal; the sweep must still get it."""
        with tempfile.TemporaryDirectory() as tmp:
            code, timed_out = self.supervise_hanging_tree(Path(tmp), self.HANG)
            self.assertTrue(timed_out)
            self.assertEqual(code, -signal.SIGTERM)
            # Both recorded processes must be gone (no live task with that identity).
            survivors = [pid for pid in self.recorded_pids(Path(tmp)) if probe._alive(pid)]
            self.assertEqual(survivors, [],
                             "an owned separate-session descendant survived the timeout")

    def test_a_descendant_that_ignores_sigterm_is_still_swept(self):
        """The review's exact hazard: SIGTERM never runs the child's cleanup of its artifact."""
        hang = self.HANG.replace("import os, subprocess, sys, time\n",
                                 "import os, signal, subprocess, sys, time\n"
                                 "signal.signal(signal.SIGTERM, signal.SIG_IGN)\n")
        with tempfile.TemporaryDirectory() as tmp:
            code, timed_out = self.supervise_hanging_tree(Path(tmp), hang)
            self.assertTrue(timed_out)
            # The child ignores SIGTERM, so terminate_group escalates to SIGKILL.
            self.assertEqual(code, -signal.SIGKILL)
            survivors = [pid for pid in self.recorded_pids(Path(tmp)) if probe._alive(pid)]
            self.assertEqual(survivors, [],
                             "a SIGTERM-ignoring owned descendant survived the timeout")


class ChildProbeTests(unittest.TestCase):
    # A pid no test may accidentally signal: the fake is never a real process group.
    PID = 4_000_001

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.sandbox = Path(self.tmp.name) / "sandbox"
        self.result = self.sandbox / "result.json"
        self.reaped = []

    def run_child(self, process, apps=(), reader_factory=None):
        def launch(artifact, sandbox, log_path, env):
            log_path.parent.mkdir(parents=True, exist_ok=True)
            log_path.write_text("launcher stderr\n")
            self.spawned_env = env
            return process

        code = probe.probe_child(Path("artifact"), self.sandbox, self.result, 5.0,
                                 probe.Budget(), launch=launch,
                                 enable=lambda: "IsEnabled=true",
                                 reader_factory=reader_factory or (lambda: reader_for(*apps)),
                                 now=Clock(step=0.05), pause=lambda seconds: None,
                                 reap=self.reaped.append)
        return code, json.loads(self.result.read_text())

    def test_success_writes_the_tree_bus_note_and_launcher_output(self):
        code, payload = self.run_child(FakeProcess(self.PID), apps=(first_run_tree(self.PID),))
        self.assertEqual(code, probe.OK)
        self.assertEqual(payload["accessibility_bus"], "IsEnabled=true")
        self.assertEqual(payload["launcher_log_tail"], "launcher stderr\n")
        self.assertIn("Download runtime", json.dumps(payload["nodes"]))
        self.assertNotIn("failure", payload)
        self.assertEqual(self.spawned_env["XDG_CONFIG_HOME"], str(self.sandbox / "config"))

    def test_a_dead_artifact_is_reported_with_the_launcher_log(self):
        code, payload = self.run_child(FakeProcess(self.PID, codes=[2], exit_code=2),
                                       apps=(first_run_tree(self.PID),))
        self.assertEqual(code, probe.FAILED)
        self.assertIn("exit code 2", payload["failure"])
        self.assertEqual(payload["launcher_log_tail"], "launcher stderr\n")

    def test_the_launchers_process_group_is_terminated_after_the_probe(self):
        process = FakeProcess(self.PID, exit_code=0)
        self.run_child(process, apps=(first_run_tree(self.PID),))
        self.assertEqual(self.reaped, [process])

    def test_the_launcher_is_reaped_even_when_the_probe_fails(self):
        process = FakeProcess(self.PID, codes=[2], exit_code=2)
        self.run_child(process, apps=(first_run_tree(self.PID),))
        self.assertEqual(self.reaped, [process])

    def test_a_gi_failure_is_an_error_and_not_a_silent_success(self):
        def explode():
            raise ImportError("no typelib for Atspi-2.0")

        code, payload = self.run_child(FakeProcess(self.PID), reader_factory=explode)
        self.assertEqual(code, probe.FAILED)
        self.assertIn("no typelib", payload["error"])

    def test_the_result_is_written_even_when_nothing_was_bound(self):
        code, payload = self.run_child(FakeProcess(self.PID), apps=())
        self.assertEqual(code, probe.FAILED)
        self.assertIn(f"no AT-SPI application bound to PID {self.PID}", payload["failure"])


class EnvelopeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.artifact = Path(self.tmp.name) / "ligandx-linux-amd64.AppImage"
        self.artifact.write_bytes(b"artifact")
        self.artifact.chmod(0o755)
        self.sandbox = Path(self.tmp.name) / "sandbox"
        patcher = mock.patch.object(probe, "missing_prerequisites", return_value=[])
        patcher.start()
        self.addCleanup(patcher.stop)

    def node(self, name, role="push-button"):
        return {"name": name, "role": role, "depth": 1, "path": "0.0", "states": ["sensitive"],
                "actions": ["click"]}

    def run_with(self, payload, code=0, timed_out=False, log_text="", expect=("Continue",)):
        def supervisor(cmd, env, log_path, timeout):
            self.last_cmd, self.last_env = cmd, env
            if payload is not None:
                Path(cmd[cmd.index("--out") + 1]).parent.mkdir(parents=True, exist_ok=True)
                Path(cmd[cmd.index("--out") + 1]).write_text(json.dumps(payload))
            log_path.parent.mkdir(parents=True, exist_ok=True)
            log_path.write_text(log_text)
            return code, timed_out

        config = probe.ProbeConfig(self.artifact, sandbox=self.sandbox, expect=expect,
                                   timeout=40.0)
        return probe.run_probe(config, supervisor=supervisor)

    def test_a_successful_probe_still_claims_no_install_or_upgrade(self):
        envelope = self.run_with({"nodes": [self.node("Set up runtime files", "heading"),
                                            self.node("Continue"),
                                            self.node("Download runtime")],
                                  "application": {"name": "ligandx", "process_id": 100}})
        self.assertEqual(envelope["reasons"], [], envelope["reasons"])
        self.assertTrue(envelope["ok"])
        self.assertFalse(envelope["claims"]["fresh_install_pass"])
        self.assertFalse(envelope["claims"]["upgrade_pass"])
        self.assertFalse(envelope["claims"]["control_activated"])
        self.assertEqual(envelope["node_count"], 3)

    def test_an_unreachable_expected_control_fails_the_probe(self):
        envelope = self.run_with({"nodes": [self.node("Help")],
                                  "application": {"name": "ligandx", "process_id": 100}})
        self.assertFalse(envelope["ok"])
        self.assertEqual(envelope["expected_controls"][0],
                         {"name": "Continue", "reachable": False, "roles": []})
        self.assertTrue(any("not reachable" in reason for reason in envelope["reasons"]))

    def test_expected_name_matching_ignores_case_and_padding(self):
        envelope = self.run_with({"nodes": [self.node("continue")],
                                  "application": {"process_id": 100}}, expect=(" Continue ",))
        self.assertTrue(envelope["ok"], envelope["reasons"])

    def test_a_missing_tree_fails_the_probe(self):
        envelope = self.run_with({})
        self.assertFalse(envelope["ok"])
        self.assertIn("no accessibility tree", " ".join(envelope["reasons"]))

    def test_an_xvfb_or_bus_failure_is_attributed_to_the_child(self):
        envelope = self.run_with(None, code=3, log_text="xvfb-run: error: Xvfb failed to start")
        self.assertFalse(envelope["ok"])
        self.assertIn("exited with code 3", " ".join(envelope["reasons"]))
        self.assertIn("Xvfb failed to start", envelope["child"]["log_tail"])

    def test_a_child_timeout_fails_the_probe(self):
        envelope = self.run_with({"nodes": [self.node("Continue")]}, code=-15, timed_out=True)
        self.assertFalse(envelope["ok"])
        self.assertTrue(envelope["child"]["timed_out"])
        self.assertIn("did not finish within 40.0s", envelope["reasons"][0])

    def test_a_digest_mismatch_refuses_to_launch_the_artifact(self):
        calls = []
        config = probe.ProbeConfig(self.artifact, sandbox=self.sandbox, timeout=40.0,
                                   expected_sha256="f" * 64)
        with self.assertRaisesRegex(probe.ProbeFailure, "does not match the expected"):
            probe.run_probe(config, supervisor=lambda *args: calls.append(args) or (0, False))
        self.assertEqual(calls, [])

    def test_the_child_is_supervised_inside_xvfb_and_a_fresh_bus(self):
        self.run_with({"nodes": [self.node("Continue")]})
        command = " ".join(self.last_cmd)
        self.assertIn("xvfb-run -a -s -screen 0 1280x800x24", command)
        self.assertIn("dbus-run-session", command)
        self.assertIn("--child", command)
        self.assertIn("--timeout 10.0", command)  # child deadline = parent deadline minus grace
        self.assertEqual(Path(self.last_cmd[self.last_cmd.index("--artifact") + 1]),
                         self.artifact)

    def test_the_envelope_declares_isolation_and_strips_production_inputs(self):
        envelope = self.run_with({"nodes": [self.node("Continue")]})
        self.assertEqual(envelope["isolation"]["xdg_sandbox"], str(self.sandbox))
        for key in ("DISPLAY", "DBUS_SESSION_BUS_ADDRESS", "LIGANDX_RUNTIME_BUNDLE_URL"):
            self.assertNotIn(key, self.last_env, key)
        # The sandbox replaces, rather than inherits, the owner's XDG roots.
        self.assertEqual(self.last_env["XDG_CONFIG_HOME"], str(self.sandbox / "config"))
        self.assertEqual(Path(self.last_env["XDG_CACHE_HOME"]).parent, self.sandbox)
        self.assertEqual(self.last_env["GTK_A11Y"], "atspi")
        self.assertIn("DISPLAY", envelope["isolation"]["stripped"])

    def test_main_returns_the_probe_verdict_and_writes_the_envelope(self):
        payload = {"nodes": [self.node("Continue")], "application": {"process_id": 100}}

        def passing(cmd, env, log_path, timeout):
            Path(cmd[cmd.index("--out") + 1]).write_text(json.dumps(payload))
            log_path.write_text("")
            return 0, False

        def failing(cmd, env, log_path, timeout):
            Path(cmd[cmd.index("--out") + 1]).write_text(json.dumps(payload))
            log_path.write_text("xvfb-run: error: Xvfb failed to start")
            return 3, False

        out = Path(self.tmp.name) / "envelope.json"
        digest = probe.record_provenance(self.artifact)["sha256"]
        argv = ["--artifact", str(self.artifact), "--sandbox", str(self.sandbox),
                "--out", str(out), "--expect", "continue", "--sha256", digest]
        with mock.patch.object(probe, "supervise", passing):
            self.assertEqual(probe.main(argv), probe.OK)
        self.assertTrue(out.is_file())
        with mock.patch.object(probe, "supervise", failing):
            self.assertEqual(probe.main(argv), probe.FAILED)


class EnvironmentTests(unittest.TestCase):
    BASE = {"DISPLAY": ":1", "DBUS_SESSION_BUS_ADDRESS": "unix:path=/real/bus",
            "XDG_CONFIG_HOME": "/home/owner/.config", "NO_AT_BRIDGE": "1",
            "LIGANDX_RUNTIME_DIR": "/preseeded/runtime", "HOME": "/home/owner",
            "PATH": "/usr/bin"}

    def test_the_child_environment_drops_the_owners_display_and_bus(self):
        env = probe.sandbox_environment(Path("/sandbox"), strip_bus=True, base=dict(self.BASE))
        self.assertNotIn("DISPLAY", env)
        self.assertNotIn("DBUS_SESSION_BUS_ADDRESS", env)
        self.assertNotIn("LIGANDX_RUNTIME_DIR", env)
        self.assertEqual(env["XDG_CONFIG_HOME"], "/sandbox/config")
        self.assertEqual(env["XDG_DATA_HOME"], "/sandbox/data")

    def test_the_artifact_environment_keeps_the_fresh_display_and_bus(self):
        base = dict(self.BASE, DISPLAY=":99", DBUS_SESSION_BUS_ADDRESS="unix:path=/new/bus")
        env = probe.sandbox_environment(Path("/sandbox"), strip_bus=False, base=base)
        self.assertEqual(env["DISPLAY"], ":99")
        self.assertEqual(env["DBUS_SESSION_BUS_ADDRESS"], "unix:path=/new/bus")
        self.assertNotIn("NO_AT_BRIDGE", env)
        self.assertEqual(env["APPIMAGE_EXTRACT_AND_RUN"], "1")
        self.assertEqual(env["GTK_A11Y"], "atspi")

    def test_enable_accessibility_reports_a_bus_refusal(self):
        ok = mock.Mock(return_value=mock.Mock(returncode=0, stdout="()", stderr=""))
        self.assertEqual(probe.enable_accessibility(run=ok), "org.a11y.Status.IsEnabled=true")
        bad = mock.Mock(return_value=mock.Mock(returncode=1, stdout="", stderr="Command failed"))
        self.assertIn("failed to set", probe.enable_accessibility(run=bad))
        self.assertIn("failed to set", probe.enable_accessibility(
            run=mock.Mock(side_effect=OSError("no gdbus"))))


class ControlPrimitiveTests(unittest.TestCase):
    """The reusable driver surface a later scenario will use; the probe never calls it."""

    def test_activate_uses_a_click_like_action(self):
        ref = FakeAccessible("push-button", "Download runtime", actions=["press"])
        node = probe.Node("push-button", "Download runtime", 0, "0", (), ("press",), ref)
        self.assertEqual(probe.activate(node), "press")
        self.assertEqual(ref.performed, [0])

    def test_activate_reports_a_refused_action(self):
        ref = FakeAccessible("push-button", "Continue", actions=["click"], fail={"do_action"})
        node = probe.Node("push-button", "Continue", 0, "0", (), ("click",), ref)
        with self.assertRaisesRegex(probe.ProbeFailure, "do_action"):
            probe.activate(node)

    def test_activate_without_a_click_like_action_fails(self):
        node = probe.Node("text", "Username", 0, "0", (), ("scroll",), object())
        with self.assertRaisesRegex(probe.ProbeFailure, "no click-like action"):
            probe.activate(node)

    def test_activate_refuses_a_detached_node(self):
        with self.assertRaisesRegex(probe.ProbeFailure, "detached"):
            probe.activate(probe.Node("push-button", "Continue", 0, "0"))

    def test_set_text_requires_an_editable_node(self):
        ref = FakeAccessible("entry", "Username", states=("editable",))
        node = probe.Node("entry", "Username", 0, "0", ("editable",), (), ref)
        probe.set_text(node, "harness")
        self.assertEqual(ref.texts, ["harness"])
        with self.assertRaisesRegex(probe.ProbeFailure, "not editable"):
            probe.set_text(probe.Node("entry", "Username", 0, "0", (), (), ref), "harness")


if __name__ == "__main__":
    unittest.main()
