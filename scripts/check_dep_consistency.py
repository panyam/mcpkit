#!/usr/bin/env python3
"""Fail when a third-party dependency is pinned at more than one version across
the published Go modules (root + SUB_MODS_TO_TAG).

Why this exists
---------------
The repo requires lock-step pins (DEPENDENCY_POLICY.md). Divergence is not a
style problem: 81 of 88 modules use local `replace` directives, so intra-repo
deps resolve locally while third-party ones resolve through MVS. MVS takes the
*maximum* required version across the graph, so a bump in one module silently
raises the version its siblings build against. If that bump is API-breaking, the
sibling's code breaks without its go.mod ever changing.

This runs in per-PR CI so divergence is caught on the PR that introduces it,
rather than weeks later as an unexplained failure in an unrelated module.

Scope
-----
Published modules only. `examples/` and `tests/` are excluded on the same rule
as .github/dependabot.yml: they are demos and harnesses, not modules anyone
imports. They are still swept by `just tidy-all` and scanned by `just vulncheck`.

mcpkit's own modules are skipped: they resolve via `replace`, so their recorded
versions are v0.0.0 pseudo-versions that differ legitimately.

Stdlib only, on purpose. This runs in the `test` job, which is the fastest job
in the matrix and has no Python tooling set up; keeping it dependency-free means
it needs no uv step.

Usage
-----
    python3 scripts/check_dep_consistency.py
    python3 scripts/check_dep_consistency.py --update-baseline
"""

from __future__ import annotations

import argparse
import collections
import json
import os
import re
import subprocess
import sys

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BASELINE = os.path.join(REPO_ROOT, "scripts", "dep-baseline.json")
INTERNAL_PREFIX = "github.com/panyam/mcpkit"

REQUIRE_RE = re.compile(r"^\s+(\S+)\s+(v\S+)")
SUB_MODS_RE = re.compile(r'SUB_MODS_TO_TAG := "([^"]+)"')


def published_modules() -> list[str]:
    """Root plus every module in the justfile's SUB_MODS_TO_TAG.

    Sourced from the justfile rather than duplicated here so the release tag set
    and the consistency gate cannot drift apart.
    """
    with open(os.path.join(REPO_ROOT, "justfile")) as fh:
        match = SUB_MODS_RE.search(fh.read())
    if not match:
        sys.exit("check_dep_consistency: SUB_MODS_TO_TAG not found in justfile")
    mods = ["."] + match.group(1).split()
    return [m for m in mods if os.path.exists(os.path.join(REPO_ROOT, m, "go.mod"))]


def collect(mods: list[str]) -> dict[str, dict[str, list[str]]]:
    versions: dict[str, dict[str, list[str]]] = collections.defaultdict(
        lambda: collections.defaultdict(list)
    )
    for mod in mods:
        with open(os.path.join(REPO_ROOT, mod, "go.mod")) as fh:
            for line in fh:
                stripped = line.strip()
                if stripped.startswith("//"):
                    continue
                match = REQUIRE_RE.match(line)
                if not match:
                    continue
                dep, ver = match.group(1), match.group(2)
                if dep.startswith(INTERNAL_PREFIX):
                    continue
                versions[dep][ver].append(mod)
    return versions


def divergent(versions) -> dict[str, dict[str, list[str]]]:
    return {d: dict(v) for d, v in versions.items() if len(v) > 1}


def all_modules() -> list[str]:
    """Every module in the tree, published or not.

    Unification runs over all of them: an example left on an older pin still
    gets silently upgraded by MVS, which is the surprise we are removing.
    """
    mods = []
    for root, dirs, files in os.walk(REPO_ROOT):
        dirs[:] = [d for d in dirs if d not in ("node_modules", ".git", ".claude")]
        if "go.mod" in files:
            mods.append(os.path.relpath(root, REPO_ROOT))
    return sorted(mods)


def semver_key(ver: str):
    """Ordering key for a Go module version.

    Handles releases, prereleases, and pseudo-versions. A release sorts above a
    prerelease of the same M.m.p, which is what semver requires and what makes
    `max()` pick the version MVS would actually resolve to.
    """
    body = ver.lstrip("v")
    pre = ""
    if "-" in body:
        body, pre = body.split("-", 1)
    nums = [int(x) for x in re.findall(r"\d+", body)[:3]]
    while len(nums) < 3:
        nums.append(0)
    return (nums[0], nums[1], nums[2], 0 if pre else 1, pre)


def unify(baseline: dict[str, list[str]]) -> int:
    """Raise every lagging module to the maximum version already in the tree.

    This is the lock-step half of the sweep. `go get -u` runs per module against
    that module's own graph, so it routinely lands modules on different patch
    levels and *introduces* divergence. Unification is what turns a pile of
    independent updates into one consistent tree.

    Baselined deps are skipped: their divergence is deliberate.
    """
    mods = all_modules()
    versions = collect(mods)
    fixed = 0

    for dep, vers in sorted(versions.items()):
        if len(vers) < 2 or dep in baseline:
            continue
        target = max(vers, key=semver_key)
        for ver, owners in sorted(vers.items()):
            if ver == target:
                continue
            for mod in sorted(owners):
                print(f"==> {mod}: {dep} {ver} -> {target}")
                proc = subprocess.run(
                    ["go", "get", f"{dep}@{target}"],
                    cwd=os.path.join(REPO_ROOT, mod),
                    capture_output=True,
                    text=True,
                )
                if proc.returncode != 0:
                    print(f"    FAILED: {proc.stderr.strip()[:300]}")
                else:
                    fixed += 1

    print(f"\nunified {fixed} module/dependency pairs across {len(mods)} modules")
    return 0


def load_baseline() -> dict[str, list[str]]:
    if not os.path.exists(BASELINE):
        return {}
    with open(BASELINE) as fh:
        return json.load(fh).get("allowed", {})


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--update-baseline",
        action="store_true",
        help="rewrite the baseline to accept current divergence",
    )
    parser.add_argument(
        "--unify",
        action="store_true",
        help="raise every lagging module to the max version already in the tree",
    )
    parser.add_argument(
        "--prune-baseline",
        action="store_true",
        help="drop baseline entries that are no longer divergent (never adds)",
    )
    args = parser.parse_args()

    if args.unify:
        return unify(load_baseline())

    mods = published_modules()
    found = divergent(collect(mods))

    # A one-way ratchet for the sweep: entries whose divergence is gone are
    # removed, but nothing new is ever accepted. Auto-accepting would let the
    # sweep silently baseline divergence it failed to unify, which is exactly
    # the thing the gate exists to surface.
    if args.prune_baseline:
        baseline = load_baseline()
        kept = {d: v for d, v in baseline.items() if d in found}
        dropped = sorted(set(baseline) - set(kept))
        payload = {
            "_comment": (
                "Known dependency-version divergence across published modules. "
                "This should shrink to empty as the monthly sweep unifies pins; "
                "regenerate with: just update-dep-baseline"
            ),
            "allowed": {d: sorted(v) for d, v in sorted(kept.items())},
        }
        with open(BASELINE, "w") as fh:
            json.dump(payload, fh, indent=2)
            fh.write("\n")
        print(f"baseline pruned: dropped {len(dropped)}, {len(kept)} remain")
        for dep in dropped:
            print(f"  dropped {dep}")
        return 0

    if args.update_baseline:
        payload = {
            "_comment": (
                "Known dependency-version divergence across published modules. "
                "This should shrink to empty as the monthly sweep unifies pins; "
                "regenerate with: just update-dep-baseline"
            ),
            "allowed": {d: sorted(v) for d, v in sorted(found.items())},
        }
        with open(BASELINE, "w") as fh:
            json.dump(payload, fh, indent=2)
            fh.write("\n")
        print(f"baseline updated: {len(found)} accepted divergences")
        return 0

    baseline = load_baseline()
    new, stale = {}, []

    for dep, vers in found.items():
        if sorted(vers) != sorted(baseline.get(dep, [])):
            new[dep] = vers
    for dep in baseline:
        if dep not in found:
            stale.append(dep)

    print(f"scanned {len(mods)} published modules")
    print(f"divergent third-party deps: {len(found)} ({len(baseline)} baselined)")

    if new:
        print("\nFAIL: dependency versions diverge across published modules.\n")
        for dep, vers in sorted(new.items()):
            print(f"  {dep}")
            for ver, owners in sorted(vers.items()):
                shown = ", ".join(sorted(owners)[:4])
                more = f" (+{len(owners) - 4} more)" if len(owners) > 4 else ""
                print(f"      {ver}: {shown}{more}")
        print(
            "\nMVS resolves third-party deps to the maximum required version, so a\n"
            "split pin means some modules silently build against a version their\n"
            "go.mod does not name. Unify them, or run `just update-dep-baseline`\n"
            "if the divergence is deliberate."
        )
        return 1

    if stale:
        print("\nFAIL: baseline lists divergence that no longer exists:")
        for dep in sorted(stale):
            print(f"  {dep}")
        print("\nRun `just update-dep-baseline` to drop the stale entries.")
        return 1

    print("OK: no unexpected divergence.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
