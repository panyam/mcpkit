#!/usr/bin/env python3
"""Collect every example's committed walkthrough bundle into docs/site's
output tree so `make docs-site-build` / `docs-site-deploy` publishes them
on gh-pages.

Discovery is purely convention-driven (no registry):

  - Walk `examples/` for any directory named `bundle/`.
  - If its parent has a sibling `walkthrough.trace.json` OR a sibling
    `walkthroughs/` directory, the bundle is recognized as a walkthrough
    bundle.
  - Mirror the bundle's contents into
    `docs/site/dist/docs/walkthroughs/<example-path>/` preserving the
    source folder layout.

URL on gh-pages mirrors the source path:

  examples/apps/compat/basic-vanillajs/bundle/index.html  ->
    https://panyam.github.io/mcpkit/walkthroughs/examples/apps/compat/basic-vanillajs/

  examples/file-inputs/bundle/happy/index.html  ->
    https://panyam.github.io/mcpkit/walkthroughs/examples/file-inputs/happy/

Multi-trace example layouts (one trace under walkthroughs/, one bundle/
subdir per trace) are mirrored as-is: bundle/<name>/index.html maps to
the URL path segment <name>.

Exit codes:
  0  one or more bundles collected (or zero bundles found, silently)
  1  bad input (missing repo root, IO error)

Runs on any platform with Python 3.9+; stdlib only.
"""

from __future__ import annotations

import shutil
import sys
from pathlib import Path

# Repo root = parent of scripts/.
MCPKIT_ROOT = Path(__file__).resolve().parent.parent
EXAMPLES_DIR = MCPKIT_ROOT / "examples"
DEST_BASE = MCPKIT_ROOT / "docs" / "site" / "dist" / "docs" / "walkthroughs"

# A bundle/ directory only gets collected if its parent has one of these
# trace-source markers as a sibling. Without them the bundle is stale
# (source-less) and the collector skips it.
TRACE_MARKERS = ("walkthrough.trace.json", "walkthroughs")


def has_trace_marker(fixture_dir: Path) -> bool:
    """True if `fixture_dir` carries a source-of-truth trace marker.

    Single-trace fixtures put walkthrough.trace.json at the fixture
    root. Multi-trace fixtures put a walkthroughs/ directory with one
    or more *.trace.json files inside.
    """
    for marker in TRACE_MARKERS:
        target = fixture_dir / marker
        if target.exists():
            return True
    return False


def discover_bundles() -> list[Path]:
    """Find every bundle/ directory under examples/ that's paired with a
    trace marker. Returns absolute paths to the bundle dirs.
    """
    if not EXAMPLES_DIR.is_dir():
        print(f"ERROR: examples directory not found at {EXAMPLES_DIR}", file=sys.stderr)
        sys.exit(1)
    bundles: list[Path] = []
    for path in EXAMPLES_DIR.rglob("bundle"):
        if not path.is_dir():
            continue
        fixture_dir = path.parent
        if not has_trace_marker(fixture_dir):
            continue
        bundles.append(path)
    return sorted(bundles)


def mirror_bundle(bundle_dir: Path) -> Path:
    """Copy bundle_dir/* into docs/site/dist/docs/walkthroughs/<fixture
    repo-relative path>/. Returns the destination directory.

    Source-tree-preserving mirror — the parent of bundle/ becomes the
    URL path segment so the on-disk and on-the-web layouts match.
    """
    fixture_dir = bundle_dir.parent
    rel = fixture_dir.relative_to(MCPKIT_ROOT)
    dest = DEST_BASE / rel
    # Clean any prior mirror so removed bundle files don't linger.
    if dest.exists():
        shutil.rmtree(dest)
    dest.mkdir(parents=True, exist_ok=True)
    for item in bundle_dir.iterdir():
        if item.is_dir():
            shutil.copytree(item, dest / item.name)
        else:
            shutil.copy2(item, dest / item.name)
    return dest


def main() -> int:
    bundles = discover_bundles()
    if not bundles:
        print("No walkthrough bundles found under examples/. Nothing to collect.")
        return 0
    print(f"Collecting {len(bundles)} walkthrough bundle(s) into {DEST_BASE.relative_to(MCPKIT_ROOT)}/")
    for bundle_dir in bundles:
        try:
            dest = mirror_bundle(bundle_dir)
        except OSError as e:
            print(f"ERROR: failed to mirror {bundle_dir}: {e}", file=sys.stderr)
            return 1
        print(f"  {bundle_dir.parent.relative_to(MCPKIT_ROOT)} -> {dest.relative_to(MCPKIT_ROOT)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
