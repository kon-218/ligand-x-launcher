#!/usr/bin/env python3
"""Render an allowlisted preview bundle without touching the stable snapshot.

The stable renderer drops every Compose service that declares profiles. This
script starts from that output. With no bundle selected, the bytes match the
stable renderer. With ``proto-mutation`` selected, it copies only the
allowlisted preview services back in, strips their profiles so they actually
start, and writes the allowlisted env keys. Unknown labels fail closed.

It does not copy the rest of the canonical env template.
"""
from __future__ import annotations

import argparse
import json
import subprocess
import sys
import tempfile
from pathlib import Path

import yaml

ALLOWLIST_PATH = Path(__file__).resolve().parents[1] / "internal" / "launcher" / "preview_bundles.json"
LEAKED_PREVIEW_SERVICES = frozenset({
    "kinetics", "worker-kinetics", "qmmm", "worker-qmmm", "licensing", "licensing-broker",
})


class PreviewBundleError(RuntimeError):
    pass


def load_allowlist(path: Path = ALLOWLIST_PATH) -> dict:
    document = json.loads(path.read_text(encoding="utf-8"))
    if document.get("schema") != "ligandx-launcher-preview-bundles/1":
        raise PreviewBundleError("unsupported preview bundle allowlist")
    bundles = document.get("bundles")
    if not isinstance(bundles, dict) or not bundles:
        raise PreviewBundleError("preview bundle allowlist is empty")
    return document


def parse_labels(raw: str) -> list[str]:
    if not raw or not raw.strip():
        return []
    labels = []
    for part in raw.split(","):
        label = part.strip()
        if not label:
            raise PreviewBundleError("unknown preview selection: empty label")
        labels.append(label)
    return labels


def require_known_labels(labels: list[str], allow: dict) -> None:
    unknown = [label for label in labels if label not in allow["bundles"]]
    if unknown:
        raise PreviewBundleError("unknown preview selection: " + ", ".join(unknown))


def apply_preview_env(template: str, labels: list[str], allow: dict | None = None) -> str:
    """Set only the allowlisted keys. Leave every other template line alone."""
    if not labels:
        return template
    allow = allow if allow is not None else load_allowlist()
    require_known_labels(labels, allow)
    wanted: dict[str, str] = {}
    for label in labels:
        env = allow["bundles"][label].get("env")
        if not isinstance(env, dict) or not env:
            raise PreviewBundleError(f"{label}: preview bundle has no env rules")
        for key, value in env.items():
            if not isinstance(key, str) or not isinstance(value, str) or not key:
                raise PreviewBundleError(f"{label}: env rules must be strings")
            wanted[key] = value
    lines = template.splitlines(keepends=True)
    seen: set[str] = set()
    for index, line in enumerate(lines):
        stripped = line.strip()
        if not stripped or stripped.startswith("#") or "=" not in stripped:
            continue
        key = stripped.partition("=")[0].strip()
        if key in wanted:
            newline = "\n" if line.endswith("\n") else ""
            lines[index] = f"{key}={wanted[key]}{newline}"
            seen.add(key)
    missing = [key for key in wanted if key not in seen]
    if missing:
        if lines and not lines[-1].endswith("\n"):
            lines[-1] += "\n"
        lines.append("# Selected preview bundle flags. Not a copy of the canonical template.\n")
        for key in missing:
            lines.append(f"{key}={wanted[key]}\n")
    return "".join(lines)


def _named_volumes(service: dict) -> set[str]:
    names: set[str] = set()
    for mount in service.get("volumes") or []:
        if not isinstance(mount, str):
            continue
        source = mount.split(":", 1)[0]
        if source and not source.startswith((".", "/", "$")):
            names.add(source)
    return names


def _render_stable(renderer: Path, canonical: Path, output: Path) -> None:
    completed = subprocess.run(
        [sys.executable, str(renderer), str(canonical), str(output)],
        capture_output=True, text=True,
    )
    if completed.returncode != 0:
        detail = completed.stderr.strip() or completed.stdout.strip()
        raise PreviewBundleError(f"stable renderer failed: {detail}")


def render_preview_bundle(
    *, canonical: Path, renderer: Path, output: Path, labels: list[str],
    allow: dict | None = None,
) -> None:
    allow = allow if allow is not None else load_allowlist()
    require_known_labels(labels, allow)
    completed = subprocess.run(
        [sys.executable, str(renderer), str(canonical), str(output),
         "--preview-bundles", ",".join(labels)], capture_output=True, text=True,
    )
    if completed.returncode:
        raise PreviewBundleError(completed.stderr.strip() or completed.stdout.strip())


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--canonical", type=Path, required=True)
    parser.add_argument("--renderer", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--bundles", default="")
    parser.add_argument("--allowlist", type=Path, default=ALLOWLIST_PATH)
    parser.add_argument("--env-template", type=Path)
    parser.add_argument("--env-output", type=Path)
    args = parser.parse_args(argv)
    try:
        allow = load_allowlist(args.allowlist)
        labels = parse_labels(args.bundles)
        render_preview_bundle(
            canonical=args.canonical, renderer=args.renderer, output=args.output,
            labels=labels, allow=allow,
        )
        if args.env_template or args.env_output:
            if not args.env_template or not args.env_output:
                raise PreviewBundleError("env template and env output are both required")
            template = args.env_template.read_text(encoding="utf-8")
            args.env_output.write_text(apply_preview_env(template, labels, allow), encoding="utf-8")
    except PreviewBundleError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
