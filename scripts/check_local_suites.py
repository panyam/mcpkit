#!/usr/bin/env python3
"""Staleness gate for conformance/local-suites.yaml.

Catches four mechanical drift cases against conformance/justfile and the
root justfile's testall stage list:

    A. Suite added to conformance/justfile as a testconf-* recipe but not
       declared in local-suites.yaml. Docs site would silently omit it.
    B. Suite declared in local-suites.yaml but no matching testconf-*
       recipe. Docs site would show a phantom row.
    C. Suite's `stage:` field in YAML does not match the stage label used
       in the testall run_stage call. Docs site would point at the wrong stage.
    E. conformance/path-defaults.just is out of sync with the YAML's
       source.default_path declarations. The generated import would
       point testconf-* recipes at the wrong worktree. Delegates to
       scripts/gen_conf_paths.py --check.

Drift case D (declared status diverges from actual run result) is not
checked here. Tracked separately.

Allowlists for targets that are NOT individual SEP suites:
    testconf, testconfall, testconfauth, testconf-tasks (v1 frozen),
    testconf-upstream-audit, testconf-elicitation,
    testconf-external-checker.

Exit codes:
    0   manifest and justfile agree
    1   drift detected, see stderr
    2   bad input (missing file, malformed YAML)

Runs on any platform with Python 3.8+ and PyYAML.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

try:
    import yaml
except ImportError:
    sys.stderr.write(
        "check_local_suites: PyYAML not available. Install with: pip install pyyaml\n"
    )
    sys.exit(2)


REPO_ROOT = Path(__file__).resolve().parent.parent
MANIFEST = REPO_ROOT / "conformance" / "local-suites.yaml"
CONF_JUSTFILE = REPO_ROOT / "conformance" / "justfile"
ROOT_JUSTFILE = REPO_ROOT / "justfile"

ALLOWLIST = {
    "testconf",  # umbrella
    "testconfall",  # umbrella
    "testconfauth",  # alias
    "testconf-tasks",  # SEP-2663 v1, frozen, no SEP coverage row
    "testconf-elicitation",  # SEP-1036, lives in conformance/elicitation/
    "testconf-upstream-audit",  # informational audit, not a SEP suite
    "testconf-external-checker",  # external client-side gauntlet vs val.town, not a per-SEP fork suite; published via conformance/EXTERNAL_CHECKER.md
}

# Matches a testconf-* recipe definition at the start of a justfile line.
TARGET_RE = re.compile(r"^(testconf[-a-z0-9]*):", re.MULTILINE)

# Matches a `run_stage X 9 name testconf-*` line in the root justfile's
# testall recipe and captures (stage, target). Tolerates both run_stage
# and run_stage_info.
RUN_STAGE_RE = re.compile(
    r"^\s*run_stage[a-z_]*\s+([0-9a-z]+)\s+\d+\s+\S+\s+(testconf[-a-z0-9]+)",
    re.MULTILINE,
)


def die(msg: str, code: int = 2) -> None:
    sys.stderr.write(f"check_local_suites: {msg}\n")
    sys.exit(code)


def load_inputs():
    if not MANIFEST.exists():
        die(f"{MANIFEST} not found")
    if not CONF_JUSTFILE.exists():
        die(f"{CONF_JUSTFILE} not found")
    if not ROOT_JUSTFILE.exists():
        die(f"{ROOT_JUSTFILE} not found")

    try:
        manifest = yaml.safe_load(MANIFEST.read_text())
    except yaml.YAMLError as exc:
        die(f"{MANIFEST}: invalid YAML: {exc}")

    if not isinstance(manifest, dict):
        die(f"{MANIFEST}: top-level value must be a mapping")

    suites = manifest.get("suites")
    if not isinstance(suites, list):
        die(f"{MANIFEST}: `suites` must be a list")

    makefile_targets = set(TARGET_RE.findall(CONF_JUSTFILE.read_text()))
    stage_map = {}
    for stage, target in RUN_STAGE_RE.findall(ROOT_JUSTFILE.read_text()):
        stage_map[target] = stage

    return suites, makefile_targets, stage_map


def main() -> int:
    suites, makefile_targets, stage_map = load_inputs()
    yaml_targets = {s["suite"] for s in suites if "suite" in s}

    drifts = []

    for t in sorted(makefile_targets - yaml_targets - ALLOWLIST):
        drifts.append(
            f"case A: {t} is a testconf recipe in conformance/justfile but has no entry in {MANIFEST.relative_to(REPO_ROOT)}"
        )

    for s in sorted(yaml_targets - makefile_targets):
        drifts.append(
            f"case B: {s} is declared in {MANIFEST.relative_to(REPO_ROOT)} but no matching testconf recipe in conformance/justfile"
        )

    for entry in suites:
        suite = entry.get("suite")
        yaml_stage = str(entry.get("stage", "")).strip()
        if not suite:
            continue
        if suite not in makefile_targets:
            continue
        actual_stage = stage_map.get(suite)
        if actual_stage is None:
            if yaml_stage != "-":
                drifts.append(
                    f'case C: {suite} declares stage={yaml_stage!r} in YAML but is not wired into testall; '
                    f'YAML should say stage: "-"'
                )
        elif actual_stage != yaml_stage:
            drifts.append(
                f"case C: {suite} declares stage={yaml_stage!r} in YAML but testall wires it at stage={actual_stage!r}"
            )

    if drifts:
        for d in drifts:
            sys.stderr.write(f"check_local_suites: {d}\n")
        sys.stderr.write("\n")
        sys.stderr.write(
            "check_local_suites: drift detected. Update conformance/local-suites.yaml,\n"
            "                    conformance/justfile, and/or the justfile testall stage\n"
            "                    list so the three sources agree. Then run:\n"
            "                      just refresh-conformance && just check-conformance-stale\n"
        )
        return 1

    # Case E: conformance/path-defaults.just must match what
    # gen_conf_paths.py would produce from the YAML. Run the generator in
    # --check mode and propagate its exit code.
    import subprocess  # local import; only needed when no earlier drift fired
    gen_script = Path(__file__).with_name("gen_conf_paths.py")
    result = subprocess.run(
        [sys.executable, str(gen_script), "--check"],
        cwd=REPO_ROOT,
    )
    if result.returncode != 0:
        return result.returncode

    print(f"check_local_suites: {len(yaml_targets)} suites checked, no drift.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
