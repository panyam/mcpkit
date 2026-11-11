#!/usr/bin/env python3
"""Fail when a Go module outside examples/ and docs/ is missing from the
Makefile's SUB_MODS_TO_TAG.

A module left out of that list gets no tag at release, so consumers can only
resolve its last tag, with that tag's old mcpkit pin. experimental/ext/protogen
dropped out in 925dc6a2 "for now" in June 2026 and went untagged until
#1319.

A module that should not be tagged goes in UNTAGGED with its reason.

Usage
-----
    python3 scripts/check_tag_set.py [repo-root]
"""

from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from check_dep_consistency import SUB_MODS_RE  # noqa: E402

# Module directories under these prefixes are demos or tools, never imported.
SKIP_PREFIXES = ("examples/", "docs/")
SKIP_PARTS = {"node_modules", "testdata", ".git"}

# Modules deliberately left untagged, each with its reason.
UNTAGGED: dict[str, str] = {}


def tagged(root: str) -> set[str]:
    with open(os.path.join(root, "Makefile")) as fh:
        match = SUB_MODS_RE.search(fh.read())
    if not match:
        sys.exit("check_tag_set: SUB_MODS_TO_TAG not found in Makefile")
    return set(match.group(1).replace("\\", " ").split())


def modules(root: str) -> list[str]:
    found = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in SKIP_PARTS]
        if "go.mod" not in filenames:
            continue
        rel = os.path.relpath(dirpath, root).replace(os.sep, "/")
        if rel == "." or rel.startswith(SKIP_PREFIXES):
            continue
        found.append(rel)
    return sorted(found)


def main() -> int:
    root = sys.argv[1] if len(sys.argv) > 1 else os.path.dirname(
        os.path.dirname(os.path.abspath(__file__)))
    tag_set = tagged(root)
    missing = [m for m in modules(root) if m not in tag_set and m not in UNTAGGED]
    stale = sorted(m for m in UNTAGGED if not os.path.exists(os.path.join(root, m, "go.mod")))
    for m in missing:
        print(f"check_tag_set: {m} is not in SUB_MODS_TO_TAG, so releases never tag it."
              " Add it there, or to UNTAGGED in scripts/check_tag_set.py with a reason.")
    for m in stale:
        print(f"check_tag_set: UNTAGGED lists {m}, which has no go.mod. Remove the entry.")
    if missing or stale:
        return 1
    print(f"check_tag_set: all {len(tag_set)} tagged modules accounted for, none missing")
    return 0


if __name__ == "__main__":
    sys.exit(main())
