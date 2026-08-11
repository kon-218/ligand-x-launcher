#!/usr/bin/env python3
"""Reject agent artifacts and broken local links in tracked documentation."""

from __future__ import annotations

import re
import subprocess
from pathlib import Path
from urllib.parse import unquote


FORBIDDEN_NAME = re.compile(
    r"(?:^|_)(?:HANDOFF|COMPLETION|IMPLEMENTATION_PLAN)(?:_|\.|$)", re.IGNORECASE
)
FORBIDDEN_CONTENT = re.compile(
    r"\b(?:superpowers|coding agent|implementation agent|next agent)\b", re.IGNORECASE
)
LINK = re.compile(r"!?\[[^\]]*\]\(([^)]+)\)")


def main() -> int:
    root = Path(__file__).resolve().parents[1]
    result = subprocess.run(
        ["git", "ls-files", "-z"], cwd=root, capture_output=True, check=True
    )
    tracked = [item for item in result.stdout.decode().split("\0") if item]
    failures: list[str] = []

    for relative in tracked:
        path = Path(relative)
        if "/superpowers/" in f"/{relative.replace(chr(92), '/')}/":
            failures.append(f"{relative}: agent plan/spec directory is forbidden")
        if path.name in {"AGENTS.md", "CLAUDE.md"} or FORBIDDEN_NAME.search(path.name):
            failures.append(f"{relative}: local agent artifact must not be tracked")

    markdown = [path for path in tracked if path.lower().endswith((".md", ".mdx"))]
    for relative in markdown:
        path = root / relative
        text = path.read_text(encoding="utf-8")
        if relative != "docs/README.md":
            for line_number, line in enumerate(text.splitlines(), start=1):
                if FORBIDDEN_CONTENT.search(line):
                    failures.append(f"{relative}:{line_number}: agent-session language is forbidden")

        in_fence = False
        for line_number, line in enumerate(text.splitlines(), start=1):
            if line.lstrip().startswith("```"):
                in_fence = not in_fence
                continue
            if in_fence:
                continue
            for match in LINK.finditer(line):
                target = match.group(1).strip()
                if target.startswith("<") and ">" in target:
                    target = target[1 : target.index(">")]
                else:
                    target = target.split(maxsplit=1)[0]
                target = unquote(target)
                if not target or target.startswith(("#", "/", "mailto:", "data:")):
                    continue
                if re.match(r"^[a-z][a-z0-9+.-]*://", target, re.IGNORECASE):
                    continue
                target = target.split("#", 1)[0].split("?", 1)[0]
                if not target:
                    continue
                resolved = (path.parent / target).resolve()
                try:
                    resolved.relative_to(root)
                except ValueError:
                    failures.append(f"{relative}:{line_number}: link escapes repository: {target}")
                    continue
                if not resolved.exists():
                    failures.append(f"{relative}:{line_number}: missing local link target: {target}")

    if failures:
        print("\n".join(failures))
        return 1
    print(f"Documentation hygiene passed for {len(markdown)} tracked Markdown files.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
