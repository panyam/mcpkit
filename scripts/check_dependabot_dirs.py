#!/usr/bin/env python3
"""Fail when a directory listed in .github/dependabot.yml no longer exists.

Why this exists
---------------
Dependabot silently ignores a configured directory that is not there. It does
not warn, and the only symptom is a dependency tree that quietly stops being
monitored.

That is not hypothetical. `/agent/web/web` was configured for npm, the
`agent/surfaces/*` refactor moved it to `agent/surfaces/web/web`, and the
config was not updated. The tree went unmonitored until an OpenSSF Scorecard
run surfaced a `@babel/core` advisory in it.

Glob patterns are resolved, so `/ext/*` passes when it matches at least one
directory holding a manifest. A glob that matches nothing is the same failure
as a missing literal path.

Stdlib only, so this can run in the fastest CI job with no extra setup.

Usage:
    python3 scripts/check_dependabot_dirs.py
"""

from __future__ import annotations

import glob
import os
import re
import sys

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CONFIG = os.path.join(REPO_ROOT, ".github", "dependabot.yml")

# The manifest each ecosystem looks for. A directory that exists but holds no
# manifest is just as unmonitored as one that is missing.
MANIFESTS = {
    "gomod": ["go.mod"],
    "npm": ["package.json"],
    "github-actions": [".github/workflows"],
    "pip": ["pyproject.toml", "requirements.txt"],
    "docker": ["Dockerfile"],
}


def parse() -> list[tuple[str, str]]:
    """Return (ecosystem, directory) pairs.

    Hand-rolled rather than pulling in PyYAML: this runs in the `test` job,
    which has no Python tooling env, and dependabot.yml is a fixed shape we
    control.
    """
    entries: list[tuple[str, str]] = []
    ecosystem = ""
    in_dirs = False

    with open(CONFIG) as fh:
        for line in fh:
            stripped = line.strip()
            if stripped.startswith("#") or not stripped:
                continue

            eco = re.match(r"-?\s*package-ecosystem:\s*[\"']?([\w-]+)", stripped)
            if eco:
                ecosystem, in_dirs = eco.group(1), False
                continue

            # Singular `directory:` is a scalar; plural `directories:` opens a list.
            one = re.match(r"directory:\s*[\"']([^\"']+)", stripped)
            if one:
                entries.append((ecosystem, one.group(1)))
                in_dirs = False
                continue

            if re.match(r"directories:\s*$", stripped):
                in_dirs = True
                continue

            if in_dirs:
                item = re.match(r"-\s*[\"']([^\"']+)", stripped)
                if item:
                    entries.append((ecosystem, item.group(1)))
                else:
                    in_dirs = False

    return entries


def resolve(ecosystem: str, directory: str) -> list[str]:
    """Directories matching `directory` that actually hold a manifest."""
    pattern = os.path.join(REPO_ROOT, directory.lstrip("/"))
    candidates = glob.glob(pattern) if any(c in directory for c in "*?[") else [pattern]

    wanted = MANIFESTS.get(ecosystem, [])
    hits = []
    for c in candidates:
        if not os.path.isdir(c):
            continue
        if not wanted or any(os.path.exists(os.path.join(c, m)) for m in wanted):
            hits.append(c)
    return hits


def main() -> int:
    entries = parse()
    if not entries:
        print("check_dependabot_dirs: parsed no entries — is the config shape unchanged?")
        return 1

    broken = []
    for ecosystem, directory in entries:
        if not resolve(ecosystem, directory):
            broken.append((ecosystem, directory))

    print(f"check_dependabot_dirs: {len(entries)} configured director{'y' if len(entries) == 1 else 'ies'} checked")

    if broken:
        print("\nFAIL: configured but not present, so silently unmonitored.\n")
        for ecosystem, directory in broken:
            manifests = " or ".join(MANIFESTS.get(ecosystem, ["(any)"]))
            print(f"  {ecosystem:<16} {directory}   (expected {manifests})")
        print(
            "\nDependabot ignores a directory it cannot find without warning. Fix the\n"
            "path, or drop the entry if the tree is gone."
        )
        return 1

    print("OK: every configured directory exists and holds a manifest.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
