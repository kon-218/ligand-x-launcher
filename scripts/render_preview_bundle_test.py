#!/usr/bin/env python3
"""Focused checks for allowlisted preview-bundle rendering."""
from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml

sys.path.insert(0, str(Path(__file__).resolve().parent))
import render_preview_bundle as preview  # noqa: E402

LAUNCHER = Path(__file__).resolve().parents[1]
RENDERER = Path(os.environ.get("LIGANDX_PUBLIC_REPO", str(LAUNCHER.parent / "ligand-x"))) / "scripts" / "render_stable_compose.py"

CANONICAL = """\
services:
  gateway:
    image: ghcr.io/kon-218/ligand-x/gateway:${VERSION}
  worker-proto:
    profiles: ["preview"]
    image: ghcr.io/kon-218/ligand-x/worker-proto:${VERSION}
    environment:
      QUEUE: proto-gpu
  worker-kinetics:
    profiles: ["preview"]
    image: ghcr.io/kon-218/ligand-x-pro/worker-kinetics:${VERSION}
  bioemu-outputs-init:
    profiles: ["preview", "preview-bioemu"]
    image: alpine:3.21
  bioemu:
    profiles: ["preview", "preview-bioemu"]
    image: ghcr.io/kon-218/ligand-x-pro/bioemu:${VERSION}
  worker-bioemu:
    profiles: ["preview", "preview-bioemu"]
    image: ghcr.io/kon-218/ligand-x-pro/worker-bioemu:${VERSION}
volumes: {}
"""

TEMPLATE = """\
VERSION=v1.2.3
UNRELATED_CANONICAL_SHOULD_NOT_APPEAR=no
WORKER_CPU_CPU_LIMIT=2
"""


class PreviewBundleRenderTest(unittest.TestCase):
    def setUp(self):
        if not RENDERER.is_file():
            self.fail(f"stable renderer not found: {RENDERER}")
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = Path(self.tmp.name)
        self.canonical = root / "docker-compose.yml"
        self.canonical.write_text(CANONICAL, encoding="utf-8")
        self.template = root / "env.template"
        self.template.write_text(TEMPLATE, encoding="utf-8")
        self.output = root / "out.yml"
        self.env_out = root / "out.env"

    def render(self, bundles: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            [
                sys.executable, str(Path(preview.__file__)),
                "--canonical", str(self.canonical),
                "--renderer", str(RENDERER),
                "--bundles", bundles,
                "--output", str(self.output),
                "--env-template", str(self.template),
                "--env-output", str(self.env_out),
            ],
            capture_output=True, text=True,
        )

    def test_stable_default_excludes_pilot_and_matches_renderer(self):
        stable = Path(self.tmp.name) / "stable.yml"
        subprocess.run(
            [sys.executable, str(RENDERER), str(self.canonical), str(stable)],
            capture_output=True, text=True, check=True,
        )
        result = self.render("")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.output.read_bytes(), stable.read_bytes())
        text = self.output.read_text(encoding="utf-8")
        self.assertNotIn("worker-proto:", text)
        self.assertNotIn("worker-kinetics:", text)
        # FEAT-27: the dormant BioEmu preview stays, under its own profile only.
        services = yaml.safe_load(text)["services"]
        self.assertEqual(services["worker-bioemu"]["profiles"], ["preview-bioemu"])
        self.assertEqual(self.env_out.read_text(encoding="utf-8"), TEMPLATE)
        self.assertNotIn("PROTO_HOME=", self.env_out.read_text(encoding="utf-8"))

    def test_proto_mutation_includes_exact_worker_and_flags(self):
        result = self.render("proto-mutation")
        self.assertEqual(result.returncode, 0, result.stderr)
        document = yaml.safe_load(self.output.read_text(encoding="utf-8"))
        services = document["services"]
        self.assertIn("gateway", services)
        self.assertIn("worker-proto", services)
        self.assertNotIn("worker-kinetics", services)
        self.assertNotIn("profiles", services["worker-proto"])
        self.assertEqual(
            services["worker-proto"]["image"],
            "ghcr.io/kon-218/ligand-x/worker-proto:${VERSION}",
        )
        self.assertEqual(services["worker-proto"]["environment"]["QUEUE"], "proto-gpu")
        self.assertIn("Preview bundles: proto-mutation", self.output.read_text(encoding="utf-8"))
        env = self.env_out.read_text(encoding="utf-8")
        self.assertIn("VERSION=v1.2.3", env)
        self.assertIn("WORKER_CPU_CPU_LIMIT=2", env)
        # The launcher turns protein_tools on in LIGANDX_PREVIEW_MODULES when the
        # group is selected; the bundle only carries its asset paths.
        self.assertNotIn("LIGANDX_PREVIEW_MODULES=protein_tools", env)
        self.assertIn("PROTO_HOME=/opt/proto", env)
        self.assertIn("PROTO_MODEL_CACHE=/models/proto", env)
        self.assertIn("UNRELATED_CANONICAL_SHOULD_NOT_APPEAR=no", env)
        self.assertNotIn("CANONICAL_ONLY_SECRET", env)

    def test_unknown_preview_label_fails_closed(self):
        result = self.render("not-a-bundle")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unknown preview selection", result.stderr)
        self.assertFalse(self.output.exists())

    def test_missing_canonical_service_fails_closed(self):
        self.canonical.write_text("services:\n  gateway:\n    image: gateway\n", encoding="utf-8")
        result = self.render("proto-mutation")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no preview service worker-proto", result.stderr)


if __name__ == "__main__":
    unittest.main()
