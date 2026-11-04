"""Unit tests for desktop_qualification with fake AT-SPI and process interfaces.

These tests prove timeout, failure, and provenance semantics without requiring
a real display, Xvfb, or the desktop binary.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest.mock import MagicMock, patch

# Ensure the scripts directory is importable
sys.path.insert(0, str(Path(__file__).parent))

from desktop_qualification import (
    AccessibilityNode,
    AtspiDriver,
    AtspiError,
    BinaryProvenance,
    DesktopQualification,
    DesktopQualificationConfig,
    PrerequisiteMissingError,
    Scenario,
    ScenarioExecutor,
    ScenarioResult,
    ScenarioStep,
    build_first_run_probe_scenario,
    build_fresh_install_scenario,
)


class FakeAtspiDriver:
    """Fake AT-SPI driver for testing scenario execution."""

    def __init__(self, app_name: str = "Ligand-X", nodes: dict[str, AccessibilityNode] | None = None):
        self.app_name = app_name
        self.nodes = nodes or {}
        self.click_log: list[str] = []
        self.type_log: list[tuple[str, str]] = []
        self._initialized = True
        self._fail_on_wait = False

    def init(self) -> None:
        self._initialized = True

    def get_desktop(self) -> AccessibilityNode:
        app = AccessibilityNode(
            role="application",
            name=self.app_name,
            description="",
            states=["active"],
            actions=[],
            children=list(self.nodes.values()),
        )
        return AccessibilityNode(
            role="desktop",
            name="",
            description="",
            states=[],
            actions=[],
            children=[app],
        )

    def find_application(self, name: str) -> AccessibilityNode | None:
        if name.lower() in self.app_name.lower():
            return self.get_desktop().children[0]
        return None

    def click(self, node: AccessibilityNode) -> None:
        if self._fail_on_wait:
            raise AtspiError("Simulated click failure")
        self.click_log.append(node.name)

    def type_text(self, node: AccessibilityNode, text: str) -> None:
        if self._fail_on_wait:
            raise AtspiError("Simulated type failure")
        self.type_log.append((node.name, text))

    def wait_for_node(self, predicate, timeout_secs: float, poll_interval: float = 0.1):
        if self._fail_on_wait:
            raise AtspiError(f"Timeout after {timeout_secs}s waiting for node")
        deadline = time.monotonic() + min(timeout_secs, 0.5)
        while time.monotonic() < deadline:
            desktop = self.get_desktop()
            found = self._search_tree(desktop, predicate)
            if found:
                return found
            time.sleep(0.05)
        raise AtspiError(f"Timeout after {timeout_secs}s waiting for node")

    def _search_tree(self, node, predicate):
        if predicate(node):
            return node
        for child in node.children:
            found = self._search_tree(child, predicate)
            if found:
                return found
        return None


class TestBinaryProvenance(unittest.TestCase):
    """Tests for binary provenance recording."""

    def test_record_existing_file(self):
        with tempfile.NamedTemporaryFile(delete=False) as f:
            f.write(b"test binary content")
            f.flush()
            path = Path(f.name)

        try:
            prov = BinaryProvenance.record(path)
            self.assertEqual(prov.path, path.resolve())
            self.assertEqual(prov.size_bytes, 19)
            self.assertEqual(len(prov.sha256), 64)
            self.assertIsInstance(prov.mtime, float)
        finally:
            path.unlink()

    def test_record_missing_file(self):
        with self.assertRaises(FileNotFoundError):
            BinaryProvenance.record(Path("/nonexistent/binary"))

    def test_to_dict(self):
        with tempfile.NamedTemporaryFile(delete=False) as f:
            f.write(b"content")
            f.flush()
            path = Path(f.name)

        try:
            prov = BinaryProvenance.record(path)
            d = prov.to_dict()
            self.assertIn("path", d)
            self.assertIn("sha256", d)
            self.assertIn("size_bytes", d)
            self.assertIn("mtime", d)
            self.assertEqual(d["size_bytes"], 7)
        finally:
            path.unlink()

    def test_sha256_deterministic(self):
        with tempfile.NamedTemporaryFile(delete=False) as f:
            f.write(b"deterministic content")
            f.flush()
            path = Path(f.name)

        try:
            prov1 = BinaryProvenance.record(path)
            prov2 = BinaryProvenance.record(path)
            self.assertEqual(prov1.sha256, prov2.sha256)
        finally:
            path.unlink()


class TestAccessibilityNode(unittest.TestCase):
    """Tests for accessibility tree node operations."""

    def test_find_by_name(self):
        child1 = AccessibilityNode("button", "OK", "", [], [])
        child2 = AccessibilityNode("button", "Cancel", "", [], [])
        parent = AccessibilityNode("dialog", "Confirm", "", [], [], [child1, child2])

        found = parent.find_by_name("Cancel")
        self.assertIsNotNone(found)
        self.assertEqual(found.name, "Cancel")

    def test_find_by_name_with_role(self):
        btn = AccessibilityNode("button", "Submit", "", [], [])
        label = AccessibilityNode("label", "Submit", "", [], [])
        parent = AccessibilityNode("form", "", "", [], [], [btn, label])

        found = parent.find_by_name("Submit", role="button")
        self.assertIsNotNone(found)
        self.assertEqual(found.role, "button")

    def test_find_by_name_not_found(self):
        parent = AccessibilityNode("frame", "Window", "", [], [])
        self.assertIsNone(parent.find_by_name("Nonexistent"))

    def test_find_by_name_nested(self):
        deep = AccessibilityNode("button", "Deep Button", "", [], [])
        mid = AccessibilityNode("panel", "", "", [], [], [deep])
        top = AccessibilityNode("frame", "App", "", [], [], [mid])

        found = top.find_by_name("Deep Button")
        self.assertIsNotNone(found)
        self.assertEqual(found.name, "Deep Button")

    def test_find_all_by_role(self):
        btn1 = AccessibilityNode("button", "OK", "", [], [])
        btn2 = AccessibilityNode("button", "Cancel", "", [], [])
        label = AccessibilityNode("label", "Text", "", [], [])
        parent = AccessibilityNode("dialog", "", "", [], [], [btn1, label, btn2])

        buttons = parent.find_all_by_role("button")
        self.assertEqual(len(buttons), 2)
        self.assertEqual({b.name for b in buttons}, {"OK", "Cancel"})

    def test_to_dict(self):
        node = AccessibilityNode("button", "Click Me", "A button", ["active"], ["click"])
        d = node.to_dict()
        self.assertEqual(d["role"], "button")
        self.assertEqual(d["name"], "Click Me")
        self.assertEqual(d["description"], "A button")
        self.assertEqual(d["states"], ["active"])
        self.assertEqual(d["actions"], ["click"])


class TestScenario(unittest.TestCase):
    """Tests for scenario definition and building."""

    def test_add_step(self):
        scenario = Scenario("test", "A test scenario")
        scenario.add_step("click", target="Button", timeout_secs=5.0)
        self.assertEqual(len(scenario.steps), 1)
        self.assertEqual(scenario.steps[0].action, "click")
        self.assertEqual(scenario.steps[0].target, "Button")

    def test_chaining(self):
        scenario = (
            Scenario("chain", "Chained scenario")
            .add_step("wait_for_window", target="App")
            .add_step("click", target="Button")
            .add_step("type", target="Input", value="text")
        )
        self.assertEqual(len(scenario.steps), 3)

    def test_first_run_probe_scenario(self):
        scenario = build_first_run_probe_scenario()
        self.assertEqual(scenario.name, "first_run_probe")
        self.assertGreater(len(scenario.steps), 0)

    def test_fresh_install_scenario(self):
        scenario = build_fresh_install_scenario()
        self.assertEqual(scenario.name, "fresh_install")
        self.assertGreater(len(scenario.steps), 0)
        actions = [s.action for s in scenario.steps]
        self.assertIn("click", actions)
        self.assertIn("type", actions)
        self.assertIn("wait_for_window", actions)


class TestScenarioExecutor(unittest.TestCase):
    """Tests for scenario execution with fake driver."""

    def test_execute_wait_for_window_success(self):
        driver = FakeAtspiDriver("Ligand-X")
        node = AccessibilityNode("frame", "Ligand-X Main", "", [], [])
        driver.nodes["Ligand-X Main"] = node
        executor = ScenarioExecutor(driver)

        scenario = Scenario("test", "test").add_step(
            "wait_for_window", target="Ligand-X", timeout_secs=1.0
        )
        results = executor.execute(scenario)

        self.assertEqual(len(results), 1)
        self.assertTrue(results[0].success)
        self.assertIn("window_name", results[0].observations)

    def test_execute_wait_for_window_timeout(self):
        driver = FakeAtspiDriver("Other App")
        driver._fail_on_wait = True
        executor = ScenarioExecutor(driver)

        scenario = Scenario("test", "test").add_step(
            "wait_for_window", target="Ligand-X", timeout_secs=0.5
        )
        results = executor.execute(scenario)

        self.assertEqual(len(results), 1)
        self.assertFalse(results[0].success)
        self.assertIn("Timeout", results[0].error)

    def test_execute_click(self):
        driver = FakeAtspiDriver("Ligand-X")
        btn = AccessibilityNode("button", "Download runtime", "", [], ["click"])
        driver.nodes["Download runtime"] = btn
        executor = ScenarioExecutor(driver)

        scenario = Scenario("test", "test").add_step("click", target="Download runtime")
        results = executor.execute(scenario)

        self.assertEqual(len(results), 1)
        self.assertTrue(results[0].success)
        self.assertEqual(driver.click_log, ["Download runtime"])

    def test_execute_click_failure(self):
        driver = FakeAtspiDriver("Ligand-X")
        driver._fail_on_wait = True
        executor = ScenarioExecutor(driver)

        scenario = Scenario("test", "test").add_step(
            "click", target="Nonexistent", timeout_secs=0.5
        )
        results = executor.execute(scenario)

        self.assertFalse(results[0].success)

    def test_execute_type(self):
        driver = FakeAtspiDriver("Ligand-X")
        input_node = AccessibilityNode("text", "Username", "", [], [])
        driver.nodes["Username"] = input_node
        executor = ScenarioExecutor(driver)

        scenario = Scenario("test", "test").add_step(
            "type", target="Username", value="harness"
        )
        results = executor.execute(scenario)

        self.assertEqual(len(results), 1)
        self.assertTrue(results[0].success)
        self.assertEqual(driver.type_log, [("Username", "harness")])

    def test_execute_dump_tree(self):
        driver = FakeAtspiDriver("Ligand-X")
        driver.nodes["Panel"] = AccessibilityNode("panel", "Content", "", [], [])
        executor = ScenarioExecutor(driver)

        scenario = Scenario("test", "test").add_step("dump_tree")
        results = executor.execute(scenario)

        self.assertTrue(results[0].success)
        self.assertIn("tree", results[0].observations)

    def test_execute_unknown_action(self):
        driver = FakeAtspiDriver("Ligand-X")
        executor = ScenarioExecutor(driver)

        scenario = Scenario("test", "test").add_step("invalid_action")
        results = executor.execute(scenario)

        self.assertFalse(results[0].success)
        self.assertIn("Unknown action", results[0].error)

    def test_execute_stops_on_failure(self):
        driver = FakeAtspiDriver("Other App")
        driver._fail_on_wait = True
        executor = ScenarioExecutor(driver)

        scenario = (
            Scenario("test", "test")
            .add_step("wait_for_window", target="Ligand-X", timeout_secs=0.3)
            .add_step("click", target="Button")
        )
        results = executor.execute(scenario)

        self.assertEqual(len(results), 1)
        self.assertFalse(results[0].success)


class TestDesktopQualificationConfig(unittest.TestCase):
    """Tests for configuration."""

    def test_config_creation(self):
        config = DesktopQualificationConfig(
            binary_path=Path("/tmp/binary"),
            xdg_config_home=Path("/tmp/config"),
            xdg_state_home=Path("/tmp/state"),
            xdg_cache_home=Path("/tmp/cache"),
            display=":99",
            timeout_secs=30.0,
        )
        self.assertEqual(config.binary_path, Path("/tmp/binary"))
        self.assertEqual(config.display, ":99")
        self.assertEqual(config.timeout_secs, 30.0)


class TestDesktopQualification(unittest.TestCase):
    """Tests for the main qualification driver."""

    def test_check_prerequisites_missing_xvfb(self):
        with tempfile.NamedTemporaryFile(delete=False) as f:
            f.write(b"binary")
            binary_path = Path(f.name)

        try:
            config = DesktopQualificationConfig(
                binary_path=binary_path,
                xdg_config_home=Path(tempfile.mkdtemp()),
                xdg_state_home=Path(tempfile.mkdtemp()),
                xdg_cache_home=Path(tempfile.mkdtemp()),
            )
            driver = DesktopQualification(config)

            with patch("shutil.which", return_value=None):
                missing = driver.check_prerequisites()

            self.assertTrue(any("xvfb" in m.lower() for m in missing))
        finally:
            binary_path.unlink()

    def test_check_prerequisites_missing_binary(self):
        config = DesktopQualificationConfig(
            binary_path=Path("/nonexistent/binary"),
            xdg_config_home=Path(tempfile.mkdtemp()),
            xdg_state_home=Path(tempfile.mkdtemp()),
            xdg_cache_home=Path(tempfile.mkdtemp()),
        )
        driver = DesktopQualification(config)
        missing = driver.check_prerequisites()
        self.assertTrue(any("binary" in m.lower() for m in missing))

    def test_record_provenance(self):
        with tempfile.NamedTemporaryFile(delete=False) as f:
            f.write(b"test binary")
            binary_path = Path(f.name)

        try:
            config = DesktopQualificationConfig(
                binary_path=binary_path,
                xdg_config_home=Path(tempfile.mkdtemp()),
                xdg_state_home=Path(tempfile.mkdtemp()),
                xdg_cache_home=Path(tempfile.mkdtemp()),
            )
            driver = DesktopQualification(config)
            prov = driver.record_provenance()

            self.assertIsNotNone(prov)
            self.assertEqual(prov.size_bytes, 11)
            self.assertEqual(len(prov.sha256), 64)
        finally:
            binary_path.unlink()

    def test_setup_xdg_isolation(self):
        config = DesktopQualificationConfig(
            binary_path=Path("/tmp/binary"),
            xdg_config_home=Path(tempfile.mkdtemp()),
            xdg_state_home=Path(tempfile.mkdtemp()),
            xdg_cache_home=Path(tempfile.mkdtemp()),
            display=":42",
        )
        driver = DesktopQualification(config)
        env = driver.setup_xdg_isolation()

        self.assertEqual(env["XDG_CONFIG_HOME"], str(config.xdg_config_home))
        self.assertEqual(env["XDG_STATE_HOME"], str(config.xdg_state_home))
        self.assertEqual(env["XDG_CACHE_HOME"], str(config.xdg_cache_home))
        self.assertEqual(env["DISPLAY"], ":42")
        self.assertEqual(env["GTK_A11Y"], "atspi")

        self.assertTrue(config.xdg_config_home.exists())
        self.assertTrue(config.xdg_state_home.exists())
        self.assertTrue(config.xdg_cache_home.exists())

    def test_run_raises_on_missing_prerequisites(self):
        config = DesktopQualificationConfig(
            binary_path=Path("/nonexistent/binary"),
            xdg_config_home=Path(tempfile.mkdtemp()),
            xdg_state_home=Path(tempfile.mkdtemp()),
            xdg_cache_home=Path(tempfile.mkdtemp()),
        )
        driver = DesktopQualification(config)

        with self.assertRaises(PrerequisiteMissingError) as ctx:
            driver.run()

        self.assertGreater(len(ctx.exception.missing), 0)


class TestPrerequisiteMissingError(unittest.TestCase):
    """Tests for the prerequisite error."""

    def test_error_message(self):
        err = PrerequisiteMissingError(["xvfb-run", "GI Atspi"])
        self.assertIn("xvfb-run", str(err))
        self.assertIn("GI Atspi", str(err))
        self.assertEqual(err.missing, ["xvfb-run", "GI Atspi"])


class TestAtspiDriver(unittest.TestCase):
    """Tests for the real AT-SPI driver (limited without display)."""

    def test_init_without_gi(self):
        driver = AtspiDriver()
        with patch.dict("sys.modules", {"gi": None}):
            with self.assertRaises(AtspiError):
                driver.init()

    def test_wrap_accessible_none(self):
        driver = AtspiDriver()
        driver._initialized = True
        driver._atspi = MagicMock()
        node = driver._wrap_accessible(None)
        self.assertEqual(node.role, "unknown")
        self.assertEqual(node.name, "")


class TestIntegration(unittest.TestCase):
    """Integration tests combining multiple components."""

    def test_full_probe_scenario_with_fake(self):
        driver = FakeAtspiDriver("Ligand-X")
        frame = AccessibilityNode("frame", "Ligand-X", "", ["active"], [])
        driver.nodes["Ligand-X"] = frame

        scenario = build_first_run_probe_scenario()
        executor = ScenarioExecutor(driver)
        results = executor.execute(scenario)

        self.assertGreater(len(results), 0)
        for r in results:
            self.assertTrue(r.success, f"Step {r.step.action} failed: {r.error}")

    def test_scenario_result_serialization(self):
        step = ScenarioStep("click", target="Button", value=None, timeout_secs=5.0)
        result = ScenarioResult(
            step=step,
            success=True,
            duration_secs=1.23,
            observations={"clicked": "Button"},
        )

        data = {
            "step": {"action": result.step.action, "target": result.step.target},
            "success": result.success,
            "error": result.error,
            "duration_secs": result.duration_secs,
            "observations": result.observations,
        }

        json_str = json.dumps(data)
        parsed = json.loads(json_str)
        self.assertTrue(parsed["success"])
        self.assertEqual(parsed["observations"]["clicked"], "Button")


class TestEdgeCases(unittest.TestCase):
    """Edge case and boundary tests."""

    def test_empty_scenario(self):
        driver = FakeAtspiDriver("Ligand-X")
        executor = ScenarioExecutor(driver)
        scenario = Scenario("empty", "Empty scenario")
        results = executor.execute(scenario)
        self.assertEqual(results, [])

    def test_zero_timeout(self):
        driver = FakeAtspiDriver("Other App")
        driver._fail_on_wait = True
        executor = ScenarioExecutor(driver)
        scenario = Scenario("test", "test").add_step(
            "wait_for_window", target="Ligand-X", timeout_secs=0.0
        )
        results = executor.execute(scenario)
        self.assertFalse(results[0].success)

    def test_node_with_empty_name(self):
        node = AccessibilityNode("button", "", "", [], [])
        self.assertIsNone(node.find_by_name("something"))

    def test_deeply_nested_find(self):
        leaf = AccessibilityNode("button", "Deep", "", [], [])
        current = leaf
        for i in range(10):
            current = AccessibilityNode("panel", f"L{i}", "", [], [], [current])
        found = current.find_by_name("Deep")
        self.assertIsNotNone(found)
        self.assertEqual(found.name, "Deep")


if __name__ == "__main__":
    unittest.main()
