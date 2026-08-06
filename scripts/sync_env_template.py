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
"""

import sys

RESOURCE_SUFFIXES = ("_CPU_LIMIT", "_CPU_RES", "_MEM_LIMIT", "_MEM_RES", "_CONCURRENCY")


def is_resource_key(key):
    return key.endswith(RESOURCE_SUFFIXES)


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


def main(argv):
    if len(argv) < 3:
        print(__doc__, file=sys.stderr)
        return 2
    canonical_path, target_path = argv[1], argv[2]
    check_only = "--check" in argv[3:]

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
        return 0

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
    with open(target_path, "w", encoding="utf-8") as handle:
        handle.writelines(lines)

    print(f"Synchronized {len(drift)} resource setting(s) into {target_path}.")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
