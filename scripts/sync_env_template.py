#!/usr/bin/env python3
"""Keep the launcher's .env.production.template hardware settings in step with
the canonical one in ligand-x/.

Why this exists: the resource limits drifted between the two copies because
sync-runtime-topology.sh synced only docker-compose.yml. The launcher ships the
template inside the runtime bundle, and the runtime bundle is the only artifact
that auto-updates, so a stale copy there is what actually reaches users — it
made the stack unstartable on an 8-thread machine (the daemon refuses any
container whose `cpus` exceeds its CPU count).

Only the hardware-sizing keys are synced, not the whole file. The two templates
differ on purpose elsewhere: the launcher pins VERSION/PRO_VERSION to the
release it ships with, while the public repo tracks `latest`. Copying wholesale
would unpin the release and break requirePinnedProductionVersion.

Usage:
    sync_env_template.py CANONICAL TARGET           # rewrite TARGET in place
    sync_env_template.py CANONICAL TARGET --check   # exit 1 on drift, no writes
    sync_env_template.py CANONICAL TARGET --preview-bundles proto-mutation
        # also set the allowlisted preview keys; does not copy the canonical template
"""

import importlib.util
import sys
from pathlib import Path

PROTO_CONTROL_KEYS = frozenset({"LIGANDX_PROTO_PILOT_ENABLED", "PROTO_HOME", "PROTO_MODEL_CACHE", "PROTO_HOME_HOST", "PROTO_MODEL_CACHE_HOST", "PROTO_RUNTIME_MANIFEST_HOST", "PROTO_RUNTIME_MANIFEST_SHA256_HOST"})

RESOURCE_SUFFIXES = ("_CPU_LIMIT", "_CPU_RES", "_MEM_LIMIT", "_MEM_RES", "_CONCURRENCY")


def is_resource_key(key):
    return key.endswith(RESOURCE_SUFFIXES) or key in PROTO_CONTROL_KEYS


def parse(path):
    """Map key -> value for live (uncommented) assignments.

    Mirrors compose's dotenv parser: the key is whatever precedes the first
    '=', trimmed, and the last definition of a key wins.
    """
    values = {}
    with open(path, encoding="utf-8") as handle:
        for line in handle:
            stripped = line.strip()
            if not stripped or stripped.startswith("#") or "=" not in stripped:
                continue
            key, _, value = stripped.partition("=")
            values[key.strip()] = value.strip()
    return values


def _preview_bundles(argv):
    if "--preview-bundles" not in argv:
        return ""
    index = argv.index("--preview-bundles")
    if index + 1 >= len(argv) or argv[index + 1].startswith("--"):
        print("ERROR: --preview-bundles requires a comma-separated label list", file=sys.stderr)
        return None
    return argv[index + 1]


def _apply_preview_env(template, labels):
    path = Path(__file__).with_name("render_preview_bundle.py")
    spec = importlib.util.spec_from_file_location("render_preview_bundle", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module.apply_preview_env(template, module.parse_labels(labels))


def main(argv):
    if len(argv) < 3:
        print(__doc__, file=sys.stderr)
        return 2
    canonical_path, target_path = argv[1], argv[2]
    check_only = "--check" in argv[3:]
    preview_bundles = _preview_bundles(argv)
    if preview_bundles is None:
        return 2

    canonical = parse(canonical_path)
    target = parse(target_path)

    wanted = {k: v for k, v in canonical.items() if is_resource_key(k)}
    drift = {k: v for k, v in wanted.items() if target.get(k) != v}
    # A key the launcher has but canonical does not is drift too: it would keep
    # a value nobody maintains, and compose's inline fallback never fires for a
    # key that is present in the env file.
    extra = sorted(k for k in target if is_resource_key(k) and k not in wanted)

    if not drift and not extra:
        print(f"Resource settings in {target_path} match {canonical_path}.")
        return _finish_preview_env(target_path, preview_bundles, check_only)

    for key in sorted(drift):
        print(f"  {key}: launcher={target.get(key)!r} canonical={wanted[key]!r}")
    for key in extra:
        print(f"  {key}: present in launcher only ({target[key]!r})")

    if check_only:
        print(
            "ERROR: .env.production.template resource settings have drifted.\n"
            "       Run: make sync-runtime-topology",
            file=sys.stderr,
        )
        return 1
    if extra:
        print(
            "ERROR: the launcher template defines resource keys the canonical one does not.\n"
            "       Resolve by hand — this script will not delete settings.",
            file=sys.stderr,
        )
        return 1

    # Rewrite in place, preserving comments, ordering and every other key.
    with open(target_path, encoding="utf-8") as handle:
        lines = handle.readlines()
    for i, line in enumerate(lines):
        stripped = line.strip()
        if not stripped or stripped.startswith("#") or "=" not in stripped:
            continue
        key = stripped.partition("=")[0].strip()
        if key in drift:
            newline = "\n" if line.endswith("\n") else ""
            lines[i] = f"{key}={drift[key]}{newline}"
    missing = {key: value for key, value in drift.items() if key not in target}
    if missing:
        if lines and not lines[-1].endswith("\n"):
            lines[-1] += "\n"
        lines.append("# Canonical prepared-runtime controls (preview remains disabled by default).\n")
        lines.extend(f"{key}={value}\n" for key, value in sorted(missing.items()))
    with open(target_path, "w", encoding="utf-8") as handle:
        handle.writelines(lines)

    print(f"Synchronized {len(drift)} resource setting(s) into {target_path}.")
    return _finish_preview_env(target_path, preview_bundles, check_only)


def _finish_preview_env(target_path, preview_bundles, check_only):
    """Apply allowlisted preview keys only. An empty selection leaves the file."""
    if not preview_bundles:
        return 0
    current = Path(target_path).read_text(encoding="utf-8")
    try:
        updated = _apply_preview_env(current, preview_bundles)
    except Exception as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1
    if updated == current:
        print(f"Preview bundle env rules ({preview_bundles}) already match {target_path}.")
        return 0
    if check_only:
        print(
            "ERROR: preview bundle env rules are not in the launcher template.\n"
            "       Run: make sync-runtime-topology PREVIEW_BUNDLES=... PREVIEW_OUTPUT=...",
            file=sys.stderr,
        )
        return 1
    Path(target_path).write_text(updated, encoding="utf-8")
    print(f"Applied preview bundle env rules ({preview_bundles}) to {target_path}.")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
