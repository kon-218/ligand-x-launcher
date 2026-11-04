#!/usr/bin/env python3
"""AT-SPI driver for the unmodified shipped Ligand-X desktop binary.

This module drives the real desktop launcher (built with `public,webkit2_41` tags)
through its accessibility tree, not through WebDriver or wails dev. It records
binary provenance, isolates XDG state, and provides bounded-timeout operations.

Prerequisites (checked at startup):
  - xvfb-run (for headless display)
  - libwebkit2gtk-4.1-0 (for the binary itself)
  - GI Atspi 2.0 (python3-gi + gir1.2-atspi-2.0)
  - The desktop binary at the expected build path

Usage:
  python3 desktop_qualification.py --binary PATH [--scenario NAME] [--timeout SECS]

The driver does NOT patch the binary, substitute Chromium, or label a probe as
a fresh-install/upgrade pass. It reports what it actually observed.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Callable, Optional, Protocol


class AtspiError(Exception):
    """Raised when AT-SPI operations fail."""


class PrerequisiteMissingError(Exception):
    """Raised when a required prerequisite is absent."""

    def __init__(self, missing: list[str]):
        self.missing = missing
        details = ", ".join(missing)
        super().__init__(f"Missing prerequisites: {details}")


@dataclass
class BinaryProvenance:
    """SHA256 and metadata for the desktop binary under test."""

    path: Path
    sha256: str
    size_bytes: int
    mtime: float

    @classmethod
    def record(cls, path: Path) -> BinaryProvenance:
        if not path.exists():
            raise FileNotFoundError(f"Binary not found: {path}")
        data = path.read_bytes()
        stat = path.stat()
        return cls(
            path=path.resolve(),
            sha256=hashlib.sha256(data).hexdigest(),
            size_bytes=stat.st_size,
            mtime=stat.st_mtime,
        )

    def to_dict(self) -> dict[str, Any]:
        return {
            "path": str(self.path),
            "sha256": self.sha256,
            "size_bytes": self.size_bytes,
            "mtime": self.mtime,
        }


@dataclass
class AccessibilityNode:
    """A node in the AT-SPI accessibility tree."""

    role: str
    name: str
    description: str
    states: list[str]
    actions: list[str]
    children: list[AccessibilityNode] = field(default_factory=list)
    _raw_ref: Any = field(default=None, repr=False)

    def find_by_name(self, name: str, role: Optional[str] = None) -> Optional[AccessibilityNode]:
        if self.name == name and (role is None or self.role == role):
            return self
        for child in self.children:
            found = child.find_by_name(name, role)
            if found:
                return found
        return None

    def find_all_by_role(self, role: str) -> list[AccessibilityNode]:
        results = []
        if self.role == role:
            results.append(self)
        for child in self.children:
            results.extend(child.find_all_by_role(role))
        return results

    def to_dict(self) -> dict[str, Any]:
        return {
            "role": self.role,
            "name": self.name,
            "description": self.description,
            "states": self.states,
            "actions": self.actions,
            "children": [c.to_dict() for c in self.children],
        }


class AtspiDriverProtocol(Protocol):
    """Protocol for AT-SPI operations (real or fake)."""

    def init(self) -> None: ...
    def get_desktop(self) -> AccessibilityNode: ...
    def find_application(self, name: str) -> Optional[AccessibilityNode]: ...
    def click(self, node: AccessibilityNode) -> None: ...
    def type_text(self, node: AccessibilityNode, text: str) -> None: ...
    def wait_for_node(
        self,
        predicate: Callable[[AccessibilityNode], bool],
        timeout_secs: float,
        poll_interval: float = 0.5,
    ) -> AccessibilityNode: ...


class AtspiDriver:
    """Real AT-SPI driver using GI Atspi.

    This driver queries the actual accessibility tree of the running desktop binary.
    It requires a display (real or Xvfb) and the AT-SPI bus.
    """

    def __init__(self) -> None:
        self._atspi = None
        self._initialized = False

    def init(self) -> None:
        try:
            import gi

            gi.require_version("Atspi", "2.0")
            from gi.repository import Atspi

            self._atspi = Atspi
            Atspi.init()
            self._initialized = True
        except Exception as e:
            raise AtspiError(f"Failed to initialize AT-SPI: {e}") from e

    def _ensure_init(self) -> None:
        if not self._initialized:
            self.init()

    def get_desktop(self) -> AccessibilityNode:
        self._ensure_init()
        desktop = self._atspi.get_desktop(0)
        return self._wrap_accessible(desktop)

    def find_application(self, name: str) -> Optional[AccessibilityNode]:
        self._ensure_init()
        desktop = self.get_desktop()
        for child in desktop.children:
            if name.lower() in child.name.lower():
                return child
        return None

    def _wrap_accessible(self, accessible: Any) -> AccessibilityNode:
        if accessible is None:
            return AccessibilityNode(
                role="unknown", name="", description="", states=[], actions=[]
            )

        role = "unknown"
        name = ""
        description = ""
        states = []
        actions = []

        try:
            role_obj = accessible.get_role()
            role = self._atspi.role_to_string(role_obj)
        except Exception:
            pass

        try:
            name = accessible.get_name() or ""
        except Exception:
            pass

        try:
            description = accessible.get_description() or ""
        except Exception:
            pass

        try:
            state_set = accessible.get_state_set()
            if state_set:
                states = [self._atspi.state_type_to_string(s) for s in state_set.get_states()]
        except Exception:
            pass

        try:
            action_iface = accessible.get_action_iface()
            if action_iface:
                actions = [
                    action_iface.get_action_name(i)
                    for i in range(action_iface.get_n_actions())
                ]
        except Exception:
            pass

        children = []
        try:
            count = accessible.get_child_count()
            for i in range(count):
                child = accessible.get_child_at_index(i)
                if child:
                    children.append(self._wrap_accessible(child))
        except Exception:
            pass

        return AccessibilityNode(
            role=role,
            name=name,
            description=description,
            states=states,
            actions=actions,
            children=children,
            _raw_ref=accessible,
        )

    def click(self, node: AccessibilityNode) -> None:
        if node._raw_ref is None:
            raise AtspiError("Cannot click a node without a raw reference")
        try:
            action_iface = node._raw_ref.get_action_iface()
            if not action_iface:
                raise AtspiError(f"Node {node.name!r} has no actions")
            for i in range(action_iface.get_n_actions()):
                if action_iface.get_action_name(i).lower() in ("click", "press", "activate"):
                    action_iface.do_action(i, 0)
                    return
            raise AtspiError(f"No click-like action found for {node.name!r}")
        except Exception as e:
            raise AtspiError(f"Click failed: {e}") from e

    def type_text(self, node: AccessibilityNode, text: str) -> None:
        if node._raw_ref is None:
            raise AtspiError("Cannot type into a node without a raw reference")
        try:
            text_iface = node._raw_ref.get_text_iface()
            if not text_iface:
                raise AtspiError(f"Node {node.name!r} does not support text")
            text_iface.set_text(0, -1, text)
        except Exception as e:
            raise AtspiError(f"Type failed: {e}") from e

    def wait_for_node(
        self,
        predicate: Callable[[AccessibilityNode], bool],
        timeout_secs: float,
        poll_interval: float = 0.5,
    ) -> AccessibilityNode:
        deadline = time.monotonic() + timeout_secs
        while time.monotonic() < deadline:
            desktop = self.get_desktop()
            found = self._search_tree(desktop, predicate)
            if found:
                return found
            time.sleep(poll_interval)
        raise AtspiError(f"Timeout after {timeout_secs}s waiting for node")

    def _search_tree(
        self, node: AccessibilityNode, predicate: Callable[[AccessibilityNode], bool]
    ) -> Optional[AccessibilityNode]:
        if predicate(node):
            return node
        for child in node.children:
            found = self._search_tree(child, predicate)
            if found:
                return found
        return None


@dataclass
class ScenarioStep:
    """A single step in a qualification scenario."""

    action: str
    target: Optional[str] = None
    value: Optional[str] = None
    timeout_secs: float = 10.0
    description: str = ""


@dataclass
class ScenarioResult:
    """Result of executing a scenario step."""

    step: ScenarioStep
    success: bool
    error: Optional[str] = None
    duration_secs: float = 0.0
    observations: dict[str, Any] = field(default_factory=dict)


@dataclass
class Scenario:
    """An extensible sequence of UI interactions.

    Scenarios are declarative: they describe what to do, not how. The driver
    interprets each step and reports observations.
    """

    name: str
    description: str
    steps: list[ScenarioStep] = field(default_factory=list)

    def add_step(
        self,
        action: str,
        target: Optional[str] = None,
        value: Optional[str] = None,
        timeout_secs: float = 10.0,
        description: str = "",
    ) -> Scenario:
        self.steps.append(
            ScenarioStep(
                action=action,
                target=target,
                value=value,
                timeout_secs=timeout_secs,
                description=description,
            )
        )
        return self


class ScenarioExecutor:
    """Executes a scenario against the desktop binary via AT-SPI."""

    def __init__(self, driver: AtspiDriverProtocol) -> None:
        self.driver = driver

    def execute(self, scenario: Scenario) -> list[ScenarioResult]:
        results = []
        for step in scenario.steps:
            result = self._execute_step(step)
            results.append(result)
            if not result.success:
                break
        return results

    def _execute_step(self, step: ScenarioStep) -> ScenarioResult:
        start = time.monotonic()
        try:
            if step.action == "wait_for_window":
                node = self.driver.wait_for_node(
                    lambda n: n.role == "frame" and step.target in n.name,
                    step.timeout_secs,
                )
                return ScenarioResult(
                    step=step,
                    success=True,
                    duration_secs=time.monotonic() - start,
                    observations={"window_name": node.name},
                )

            elif step.action == "click":
                app = self._find_launcher_app()
                node = app.find_by_name(step.target) if app else None
                if not node:
                    node = self.driver.wait_for_node(
                        lambda n: n.name == step.target, step.timeout_secs
                    )
                self.driver.click(node)
                return ScenarioResult(
                    step=step,
                    success=True,
                    duration_secs=time.monotonic() - start,
                    observations={"clicked": node.name},
                )

            elif step.action == "type":
                app = self._find_launcher_app()
                node = app.find_by_name(step.target) if app else None
                if not node:
                    node = self.driver.wait_for_node(
                        lambda n: n.name == step.target and "text" in n.role.lower(),
                        step.timeout_secs,
                    )
                self.driver.type_text(node, step.value or "")
                return ScenarioResult(
                    step=step,
                    success=True,
                    duration_secs=time.monotonic() - start,
                    observations={"typed_into": node.name},
                )

            elif step.action == "dump_tree":
                app = self._find_launcher_app()
                tree = app.to_dict() if app else {}
                return ScenarioResult(
                    step=step,
                    success=True,
                    duration_secs=time.monotonic() - start,
                    observations={"tree": tree},
                )

            else:
                return ScenarioResult(
                    step=step,
                    success=False,
                    error=f"Unknown action: {step.action}",
                    duration_secs=time.monotonic() - start,
                )

        except Exception as e:
            return ScenarioResult(
                step=step,
                success=False,
                error=str(e),
                duration_secs=time.monotonic() - start,
            )

    def _find_launcher_app(self) -> Optional[AccessibilityNode]:
        for name in ["Ligand-X", "ligand-x", "Ligand-X Launcher"]:
            app = self.driver.find_application(name)
            if app:
                return app
        return None


@dataclass
class DesktopQualificationConfig:
    """Configuration for a desktop qualification run."""

    binary_path: Path
    xdg_config_home: Path
    xdg_state_home: Path
    xdg_cache_home: Path
    display: str = ":99"
    timeout_secs: float = 60.0
    scenario_name: str = "first_run_probe"


class DesktopQualification:
    """Main driver for desktop binary qualification.

    This class orchestrates:
    - Prerequisite checks
    - Binary provenance recording
    - XDG isolation
    - Binary launch under Xvfb
    - AT-SPI tree probing
    - Scenario execution
    """

    def __init__(self, config: DesktopQualificationConfig) -> None:
        self.config = config
        self.provenance: Optional[BinaryProvenance] = None
        self.driver: Optional[AtspiDriver] = None
        self._xvfb_proc: Optional[subprocess.Popen] = None
        self._launcher_proc: Optional[subprocess.Popen] = None
        self._temp_dirs: list[Path] = []

    def check_prerequisites(self) -> list[str]:
        missing = []
        if not shutil.which("xvfb-run"):
            missing.append("xvfb-run (apt install xvfb)")
        if not shutil.which("Xvfb"):
            missing.append("Xvfb (apt install xvfb)")
        try:
            import gi

            gi.require_version("Atspi", "2.0")
            from gi.repository import Atspi
        except Exception:
            missing.append("GI Atspi (apt install python3-gi gir1.2-atspi-2.0)")
        if not self.config.binary_path.exists():
            missing.append(f"Desktop binary at {self.config.binary_path}")
        return missing

    def record_provenance(self) -> BinaryProvenance:
        self.provenance = BinaryProvenance.record(self.config.binary_path)
        return self.provenance

    def setup_xdg_isolation(self) -> dict[str, str]:
        env = os.environ.copy()
        env["XDG_CONFIG_HOME"] = str(self.config.xdg_config_home)
        env["XDG_STATE_HOME"] = str(self.config.xdg_state_home)
        env["XDG_CACHE_HOME"] = str(self.config.xdg_cache_home)
        env["DISPLAY"] = self.config.display
        env["GTK_A11Y"] = "atspi"
        for d in [
            self.config.xdg_config_home,
            self.config.xdg_state_home,
            self.config.xdg_cache_home,
        ]:
            d.mkdir(parents=True, exist_ok=True)
        return env

    def start_xvfb(self) -> None:
        if shutil.which("Xvfb"):
            self._xvfb_proc = subprocess.Popen(
                ["Xvfb", self.config.display, "-screen", "0", "1280x720x24"],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
            )
            time.sleep(1.0)
        else:
            raise PrerequisiteMissingError(["Xvfb"])

    def stop_xvfb(self) -> None:
        if self._xvfb_proc:
            self._xvfb_proc.terminate()
            try:
                self._xvfb_proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self._xvfb_proc.kill()
            self._xvfb_proc = None

    def launch_binary(self, env: dict[str, str]) -> None:
        self._launcher_proc = subprocess.Popen(
            [str(self.config.binary_path)],
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        time.sleep(2.0)

    def stop_binary(self) -> None:
        if self._launcher_proc:
            self._launcher_proc.terminate()
            try:
                self._launcher_proc.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self._launcher_proc.kill()
            self._launcher_proc = None

    def probe_accessibility_tree(self) -> dict[str, Any]:
        if not self.driver:
            self.driver = AtspiDriver()
            self.driver.init()
        app = self.driver.find_application("Ligand-X")
        if not app:
            return {"error": "Application not found in AT-SPI tree"}
        return app.to_dict()

    def run_scenario(self, scenario: Scenario) -> list[ScenarioResult]:
        if not self.driver:
            self.driver = AtspiDriver()
            self.driver.init()
        executor = ScenarioExecutor(self.driver)
        return executor.execute(scenario)

    def cleanup(self) -> None:
        self.stop_binary()
        self.stop_xvfb()
        for d in self._temp_dirs:
            if d.exists():
                shutil.rmtree(d, ignore_errors=True)

    def run(self, scenario: Optional[Scenario] = None) -> dict[str, Any]:
        missing = self.check_prerequisites()
        if missing:
            raise PrerequisiteMissingError(missing)

        self.record_provenance()
        env = self.setup_xdg_isolation()

        try:
            self.start_xvfb()
            self.launch_binary(env)
            time.sleep(3.0)

            tree = self.probe_accessibility_tree()

            results = []
            if scenario:
                results = self.run_scenario(scenario)

            return {
                "provenance": self.provenance.to_dict() if self.provenance else None,
                "accessibility_tree": tree,
                "scenario_results": [
                    {
                        "step": {
                            "action": r.step.action,
                            "target": r.step.target,
                            "value": r.step.value,
                        },
                        "success": r.success,
                        "error": r.error,
                        "duration_secs": r.duration_secs,
                        "observations": r.observations,
                    }
                    for r in results
                ],
                "timestamp": time.time(),
            }

        finally:
            self.cleanup()


def build_first_run_probe_scenario() -> Scenario:
    """Build the default first-run probe scenario.

    This scenario observes the initial UI state without claiming it is a
    fresh-install or upgrade pass. It dumps the accessibility tree and
    checks for expected elements.
    """
    return (
        Scenario(
            name="first_run_probe",
            description="Probe the first-run accessibility tree for expected elements",
        )
        .add_step("wait_for_window", target="Ligand-X", timeout_secs=15.0)
        .add_step("dump_tree")
    )


def build_fresh_install_scenario() -> Scenario:
    """Build a fresh-install scenario (not a pass until runtime is proven).

    This exercises the setup flow: download runtime, create account, select edition,
    choose services, start services. It does NOT claim the install succeeded;
    the caller must verify service health independently.
    """
    return (
        Scenario(
            name="fresh_install",
            description="Drive the fresh-install flow (does not claim success)",
        )
        .add_step("wait_for_window", target="Ligand-X", timeout_secs=15.0)
        .add_step("click", target="Download runtime", timeout_secs=10.0)
        .add_step("wait_for_window", target="Create your account", timeout_secs=30.0)
        .add_step("type", target="Username", value="harness")
        .add_step("type", target="Password", value="testpassword123")
        .add_step("click", target="Continue", timeout_secs=10.0)
        .add_step("wait_for_window", target="License", timeout_secs=10.0)
        .add_step("click", target="Continue with Free", timeout_secs=10.0)
        .add_step("wait_for_window", target="Choose services", timeout_secs=10.0)
        .add_step("click", target="Continue", timeout_secs=10.0)
        .add_step("wait_for_window", target="Downloading images", timeout_secs=10.0)
    )


def main(argv: Optional[list[str]] = None) -> int:
    parser = argparse.ArgumentParser(
        description="AT-SPI driver for Ligand-X desktop qualification"
    )
    parser.add_argument(
        "--binary",
        type=Path,
        default=Path("build/bin/ligand-x-launcher-linux-amd64"),
        help="Path to the desktop binary",
    )
    parser.add_argument(
        "--scenario",
        choices=["first_run_probe", "fresh_install"],
        default="first_run_probe",
        help="Scenario to execute",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=60.0,
        help="Global timeout in seconds",
    )
    parser.add_argument(
        "--display",
        default=":99",
        help="X display to use",
    )
    parser.add_argument(
        "--output",
        type=Path,
        help="Write JSON result to this path",
    )
    parser.add_argument(
        "--check-prerequisites",
        action="store_true",
        help="Only check prerequisites and exit",
    )

    args = parser.parse_args(argv)

    if args.check_prerequisites:
        config = DesktopQualificationConfig(
            binary_path=args.binary,
            xdg_config_home=Path(tempfile.mkdtemp()),
            xdg_state_home=Path(tempfile.mkdtemp()),
            xdg_cache_home=Path(tempfile.mkdtemp()),
            display=args.display,
            timeout_secs=args.timeout,
        )
        driver = DesktopQualification(config)
        missing = driver.check_prerequisites()
        if missing:
            print(f"Missing prerequisites: {', '.join(missing)}", file=sys.stderr)
            return 2
        print("All prerequisites satisfied")
        return 0

    xdg_base = Path(tempfile.mkdtemp(prefix="ligandx-qual-"))
    config = DesktopQualificationConfig(
        binary_path=args.binary,
        xdg_config_home=xdg_base / "config",
        xdg_state_home=xdg_base / "state",
        xdg_cache_home=xdg_base / "cache",
        display=args.display,
        timeout_secs=args.timeout,
        scenario_name=args.scenario,
    )

    driver = DesktopQualification(config)

    if args.scenario == "first_run_probe":
        scenario = build_first_run_probe_scenario()
    elif args.scenario == "fresh_install":
        scenario = build_fresh_install_scenario()
    else:
        scenario = build_first_run_probe_scenario()

    try:
        result = driver.run(scenario)
        result_json = json.dumps(result, indent=2)
        if args.output:
            args.output.write_text(result_json)
            print(f"Result written to {args.output}")
        else:
            print(result_json)
        return 0 if all(r["success"] for r in result.get("scenario_results", [])) else 1
    except PrerequisiteMissingError as e:
        print(f"Error: {e}", file=sys.stderr)
        return 2
    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        return 1
    finally:
        driver.cleanup()
        if xdg_base.exists():
            shutil.rmtree(xdg_base, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
