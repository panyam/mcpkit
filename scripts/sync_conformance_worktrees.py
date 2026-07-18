#!/usr/bin/env python3
"""Pre-flight gate for conformance suites.

Validates that every MCPCONFORMANCE_*_PATH worktree exists and contains
the test files / build artifacts that conformance/justfile expects to run.

Catches the class of bug where:
- A fork branch refactors its test layout, and the justfile's hardcoded
  `npx vitest run <file>` path becomes stale.
- A worktree is missing entirely.
- A worktree drifts to a different branch than local-suites.yaml documents.
- An uncommitted change in a worktree would silently affect test results.

Does NOT auto-fix anything — surfaces problems with remediation hints so
fixes are explicit decisions, not silent state changes.

Reads:
- conformance/local-suites.yaml  — worktree paths + expected branch per suite
- conformance/justfile           — actual test-file paths the recipes run

Usage:
    uv run scripts/sync_conformance_worktrees.py [--fetch]

Options:
    --fetch    Run `git fetch --all --prune` in each worktree first,
               then check whether the worktree's current branch is
               behind origin/<branch>. Without --fetch, the script
               only checks state that's already local.

Exit codes:
    0   all worktrees ready to run conformance against
    1   one or more worktrees have blocking issues
    2   manifest / script error
"""

from __future__ import annotations

import argparse
import re
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

try:
    import yaml
except ImportError:
    sys.stderr.write(
        "sync_conformance_worktrees: PyYAML not available. "
        "Install with: pip install pyyaml\n"
    )
    sys.exit(2)


REPO_ROOT = Path(__file__).resolve().parent.parent
MANIFEST = REPO_ROOT / "conformance" / "local-suites.yaml"
JUSTFILE = REPO_ROOT / "conformance" / "justfile"


@dataclass
class Suite:
    name: str
    path_var: str
    default_path: Path
    branch: str | None
    artifacts: list[str] = field(default_factory=list)


@dataclass
class Status:
    suite: str
    worktree_exists: bool
    branch_actual: str | None
    blockers: list[str] = field(default_factory=list)
    warnings: list[str] = field(default_factory=list)


def load_yaml() -> dict[str, Suite]:
    with MANIFEST.open() as f:
        manifest = yaml.safe_load(f)
    suites: dict[str, Suite] = {}
    for entry in manifest.get("suites", []):
        src = entry.get("source")
        if not src or "default_path" not in src:
            continue
        rel = src["default_path"]
        suites[entry["suite"]] = Suite(
            name=entry["suite"],
            path_var=src.get("path_var", ""),
            default_path=(REPO_ROOT / rel).resolve(),
            branch=src.get("branch"),
        )
    return suites


def extract_makefile_artifacts(suites: dict[str, Suite]) -> None:
    """Walk conformance/justfile, attribute `npx vitest run X` and `node dist/X`
    references to the most-recently-opened `(cd {{MCPCONFORMANCE_*_PATH}} ...)`
    shell block."""
    var_to_suites: dict[str, list[str]] = {}
    for name, s in suites.items():
        if s.path_var:
            var_to_suites.setdefault(s.path_var, []).append(name)

    text = JUSTFILE.read_text()
    current_var: str | None = None
    paren_depth = 0

    for line in text.splitlines():
        # Track shell-block context. Lines like `(cd $(VAR) && ...)` open a
        # block; closing `)` ends it. We track depth so multi-line blocks
        # (continuation with `\`) keep the var bound.
        var_open = re.search(r"\(\s*cd\s+\{\{(MCPCONFORMANCE_\w+_PATH)\}\}", line)
        any_cd = re.search(r"\(\s*cd\s+\{\{\w+\}\}", line)
        if var_open:
            current_var = var_open.group(1)
            paren_depth = 1
        elif any_cd and not var_open:
            current_var = None
            paren_depth = 0

        if current_var is None:
            continue

        # Strip continuation backslash for cleaner artifact extraction.
        stripped = line.rstrip("\\").rstrip()

        vmatch = re.search(r"npx\s+vitest\s+run\s+(\S+)", stripped)
        if vmatch:
            # Strip trailing shell punctuation: `);`, `)`, `;`, `\`.
            artifact = vmatch.group(1).rstrip(");\\").strip()
            for suite_name in var_to_suites.get(current_var, []):
                if artifact not in suites[suite_name].artifacts:
                    suites[suite_name].artifacts.append(artifact)

        nmatch = re.search(r"\bnode\s+(dist/[\w./-]+)", stripped)
        if nmatch:
            artifact = nmatch.group(1).rstrip(");\\").strip()
            for suite_name in var_to_suites.get(current_var, []):
                if artifact not in suites[suite_name].artifacts:
                    suites[suite_name].artifacts.append(artifact)

        # Close on a line ending with `)` that isn't continued.
        if stripped.endswith(")") and not line.endswith("\\"):
            current_var = None


def git(repo: Path, *args: str, check: bool = False) -> str:
    result = subprocess.run(
        ["git", *args],
        cwd=repo,
        capture_output=True,
        text=True,
        check=check,
    )
    return result.stdout.strip()


def check_worktree(suite: Suite, do_fetch: bool) -> Status:
    s = Status(suite=suite.name, worktree_exists=False, branch_actual=None)

    if not suite.default_path.exists():
        s.blockers.append(
            f"Worktree missing: {suite.default_path}. "
            f"Clone https://github.com/panyam/mcpconformance there."
        )
        return s
    s.worktree_exists = True

    # Current branch
    try:
        s.branch_actual = git(suite.default_path, "rev-parse", "--abbrev-ref", "HEAD")
    except subprocess.CalledProcessError:
        s.blockers.append("Not a git working tree (no HEAD).")
        return s

    # Branch mismatch — warning, not blocker (might be intentional)
    if suite.branch and s.branch_actual and s.branch_actual != suite.branch:
        s.warnings.append(
            f"Branch drift: YAML expects '{suite.branch}', actually on '{s.branch_actual}'."
        )

    # Uncommitted changes — blocker
    porcelain = git(suite.default_path, "status", "--porcelain")
    if porcelain:
        n = len(porcelain.splitlines())
        s.blockers.append(
            f"Uncommitted changes ({n} files). Commit or stash before running."
        )

    if do_fetch:
        subprocess.run(
            ["git", "fetch", "--all", "--prune", "--quiet"],
            cwd=suite.default_path,
            check=False,
        )

    # Behind origin/<branch> — blocker. Skip on detached HEAD (no upstream
    # branch to compare against) and on remotes without a matching ref.
    if s.branch_actual and s.branch_actual != "HEAD":
        remote_ref = f"origin/{s.branch_actual}"
        ref_exists = subprocess.run(
            ["git", "show-ref", "--verify", "--quiet", f"refs/remotes/{remote_ref}"],
            cwd=suite.default_path,
        ).returncode == 0
        if ref_exists:
            behind = git(
                suite.default_path,
                "rev-list", "--count",
                f"HEAD..{remote_ref}",
            )
            if behind and behind != "0":
                s.blockers.append(
                    f"{behind} commits behind {remote_ref}. "
                    f"Run: cd {suite.default_path} && git pull --ff-only"
                )

    # Behind upstream/main — warning (informational, often desired post-rebase)
    try:
        behind_up = git(suite.default_path, "rev-list", "--count", "HEAD..upstream/main")
        if behind_up and behind_up != "0":
            s.warnings.append(
                f"{behind_up} commits behind upstream/main (consider rebasing if you "
                f"want recent upstream changes)."
            )
    except subprocess.CalledProcessError:
        pass  # upstream remote may not exist

    # Artifact existence — blocker. This is the load-bearing check.
    for art in suite.artifacts:
        full = suite.default_path / art
        if not full.exists():
            s.blockers.append(
                f"Missing artifact: {art} "
                f"(justfile expects {full}). The fork branch may have "
                f"refactored its layout — update conformance/justfile or "
                f"`git checkout` a branch that has this file."
            )

    return s


def main() -> int:
    parser = argparse.ArgumentParser(
        description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "--fetch", action="store_true",
        help="git fetch each worktree before checking (slower but catches behind-origin)",
    )
    args = parser.parse_args()

    if not MANIFEST.exists():
        sys.stderr.write(f"Manifest not found: {MANIFEST}\n")
        return 2
    if not JUSTFILE.exists():
        sys.stderr.write(f"justfile not found: {JUSTFILE}\n")
        return 2

    suites = load_yaml()
    if not suites:
        sys.stderr.write(f"No suites with worktrees found in {MANIFEST}\n")
        return 2

    extract_makefile_artifacts(suites)

    blocker_count = 0
    warning_count = 0

    print(f"{'Suite':<28} {'Worktree':<8} {'Branch':<32} {'Artifacts':<10} Status")
    print("-" * 95)
    for name in sorted(suites):
        suite = suites[name]
        st = check_worktree(suite, args.fetch)
        wt = "OK" if st.worktree_exists else "MISS"
        br = st.branch_actual or "-"
        if len(br) > 30:
            br = br[:27] + "..."
        n_art = len(suite.artifacts)
        if st.blockers:
            status_label = f"FAIL ({len(st.blockers)})"
            blocker_count += 1
        elif st.warnings:
            status_label = f"WARN ({len(st.warnings)})"
            warning_count += 1
        else:
            status_label = "OK"
        print(f"{name:<28} {wt:<8} {br:<32} {n_art:<10} {status_label}")
        for b in st.blockers:
            print(f"  [blocker] {b}")
        for w in st.warnings:
            print(f"  [warn]    {w}")

    print()
    print(
        f"{blocker_count} suite(s) with blockers, "
        f"{warning_count} with warnings only."
    )
    if blocker_count:
        print(
            "\nGate FAILED. Fix the blockers above before running "
            "`just conformance/test`."
        )
        return 1
    print("Gate PASS.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
