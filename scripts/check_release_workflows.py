#!/usr/bin/env python3
"""Did the tag-triggered workflows actually run for this release?

A release that triggers no workflows looks exactly like a release whose
workflows all passed: green repo, published tag, nothing red anywhere. That is
how v0.4.0 through v0.6.0 shipped with `publish-images.yml` never having run
once and `vulncheck.yml` only ever firing on its weekly schedule. `make
tag-push` sent 20 refs in a single `git push`, and GitHub raises no push event
for a tag push that large, so both `tags: ['v*']` triggers were inert (#1412).

#1411 split the push, so the mechanism works now. This is the detector for the
next time it stops working, in whatever new way.

The workflow list is derived from the tree rather than hardcoded: any workflow
whose `on.push.tags` filter matches the version being checked is in scope, so a
third tag-triggered workflow is covered the day it is added.

Usage:
    scripts/check_release_workflows.py v0.6.0     # check a released tag
    scripts/check_release_workflows.py --selftest # gate the checker itself
"""
from __future__ import annotations

import fnmatch
import json
import os
import subprocess
import sys

import yaml

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
WORKFLOW_DIR = os.path.join(ROOT, ".github", "workflows")
REPO = "panyam/mcpkit"

OK, FAILED, PENDING, MISSING = "ok", "failed", "pending", "missing"


def tag_triggered_workflows(sources: dict[str, str], tag: str) -> list[str]:
    """Which of these workflows claim to fire on `tag`.

    `sources` maps a workflow filename to its YAML text. A workflow qualifies
    when some pattern under `on.push.tags` matches the tag and no pattern under
    `on.push.tags-ignore` does.
    """
    matched = []
    for name, text in sources.items():
        doc = yaml.safe_load(text) or {}
        # `on` unquoted is the YAML 1.1 boolean True, so both spellings appear
        # in the wild and the same file can switch between them on a reformat.
        triggers = doc.get("on", doc.get(True)) or {}
        push = triggers.get("push") if isinstance(triggers, dict) else None
        if not isinstance(push, dict):
            continue
        if any(fnmatch.fnmatch(tag, p) for p in as_patterns(push.get("tags-ignore"))):
            continue
        if any(fnmatch.fnmatch(tag, p) for p in as_patterns(push.get("tags"))):
            matched.append(name)
    return sorted(matched)


def as_patterns(value) -> list[str]:
    """A `tags:` filter is a list, but a single pattern may be written bare."""
    if value is None:
        return []
    return [value] if isinstance(value, str) else list(value)


def assess_runs(runs: list[dict], tag: str) -> tuple[str, str]:
    """Reduce a workflow's run list to a verdict about `tag`.

    Returns one of OK / FAILED / PENDING / MISSING plus a line of detail. A run
    counts as being about the tag when its `head_branch` is the tag name, which
    is what GitHub reports for a tag push and for a dispatch against a tag ref.
    """
    mine = [r for r in runs if r.get("head_branch") == tag]
    if not mine:
        return MISSING, f"no run against {tag} among the last {len(runs)}"

    # A dispatch counts. It is a legitimate way to cover a release, and it is
    # how #1391 was closed, so refusing it would report a covered release as a
    # gap. The event is printed so a human can see it took a hand to get there.
    events = ",".join(sorted({r.get("event", "?") for r in mine}))
    if any(r.get("conclusion") == "success" for r in mine):
        return OK, f"{len(mine)} run(s) via {events}"
    if any(r.get("status") != "completed" for r in mine):
        return PENDING, f"still running via {events}"
    worst = ",".join(sorted({str(r.get("conclusion")) for r in mine}))
    return FAILED, f"{len(mine)} run(s) via {events}, concluded {worst}"


# Cases that pin down both halves. The YAML ones are regression guards as much
# as tests: `on:` is the single nastiest key in the GitHub Actions schema,
# because YAML 1.1 reads a bare `on` as the boolean True and pyyaml duly hands
# back a dict keyed by True rather than by the string. A checker that looks up
# "on" finds nothing and reports every workflow as untriggered, which is the
# same all-clear it would print if the workflows were genuinely fine.
#
# (label, sources, tag, expected)
SELFTEST_DISCOVERY = [
    (
        "tags: ['v*'] matches a version tag",
        {"vulncheck.yml": "on:\n  push:\n    tags: ['v*']\n"},
        "v0.6.0",
        ["vulncheck.yml"],
    ),
    (
        "quoted 'on' key, same answer",
        {"vulncheck.yml": "'on':\n  push:\n    tags: ['v*']\n"},
        "v0.6.0",
        ["vulncheck.yml"],
    ),
    (
        "schedule and dispatch only, no tag trigger",
        {"scorecard.yml": "on:\n  schedule:\n    - cron: '30 2 * * 1'\n  workflow_dispatch:\n"},
        "v0.6.0",
        [],
    ),
    (
        "push to branches is not a tag trigger",
        {"test.yml": "on:\n  push:\n    branches: [main]\n  pull_request:\n"},
        "v0.6.0",
        [],
    ),
    (
        "a tag filter that does not match the version is out of scope",
        {"demo.yml": "on:\n  push:\n    tags: ['demo-*']\n"},
        "v0.6.0",
        [],
    ),
    (
        "tags-ignore wins over a matching tags pattern",
        {"pub.yml": "on:\n  push:\n    tags: ['v*']\n    tags-ignore: ['v*-b*']\n"},
        "v0.7.0-b1",
        [],
    ),
    (
        "two in scope, sorted",
        {
            "publish-images.yml": "on:\n  push:\n    tags: ['v*']\n",
            "vulncheck.yml": "on:\n  schedule:\n    - cron: '30 6 * * 1'\n  push:\n    tags: ['v*']\n",
        },
        "v0.6.0",
        ["publish-images.yml", "vulncheck.yml"],
    ),
]

# (label, runs, tag, expected status)
SELFTEST_RUNS = [
    (
        "a successful push run against the tag",
        [{"event": "push", "head_branch": "v0.6.0", "status": "completed", "conclusion": "success"}],
        "v0.6.0",
        OK,
    ),
    (
        "a manual dispatch against the tag ref still counts",
        [{"event": "workflow_dispatch", "head_branch": "v0.6.0", "status": "completed", "conclusion": "success"}],
        "v0.6.0",
        OK,
    ),
    (
        "fired and failed is not missing, and is not ok either",
        [{"event": "push", "head_branch": "v0.6.0", "status": "completed", "conclusion": "failure"}],
        "v0.6.0",
        FAILED,
    ),
    (
        "still running",
        [{"event": "push", "head_branch": "v0.6.0", "status": "in_progress", "conclusion": None}],
        "v0.6.0",
        PENDING,
    ),
    (
        "the v0.6.0 case: runs exist, none of them about the tag",
        [
            {"event": "schedule", "head_branch": "main", "status": "completed", "conclusion": "success"},
            {"event": "workflow_dispatch", "head_branch": "main", "status": "completed", "conclusion": "success"},
        ],
        "v0.6.0",
        MISSING,
    ),
    (
        "a run against a different tag does not count",
        [{"event": "push", "head_branch": "v0.5.2", "status": "completed", "conclusion": "success"}],
        "v0.6.0",
        MISSING,
    ),
    (
        "no runs at all",
        [],
        "v0.6.0",
        MISSING,
    ),
    (
        "one success is enough when an earlier attempt failed",
        [
            {"event": "push", "head_branch": "v0.6.0", "status": "completed", "conclusion": "success"},
            {"event": "push", "head_branch": "v0.6.0", "status": "completed", "conclusion": "failure"},
        ],
        "v0.6.0",
        OK,
    ),
]


def selftest() -> int:
    failures = 0
    for label, sources, tag, expected in SELFTEST_DISCOVERY:
        got = tag_triggered_workflows(sources, tag)
        if got != expected:
            failures += 1
            print(f"selftest: {label}\n  expected {expected}, got {got}")
    for label, runs, tag, expected in SELFTEST_RUNS:
        got, _ = assess_runs(runs, tag)
        if got != expected:
            failures += 1
            print(f"selftest: {label}\n  expected {expected}, got {got}")
    total = len(SELFTEST_DISCOVERY) + len(SELFTEST_RUNS)
    print(f"selftest: {total - failures}/{total} cases pass")
    return 1 if failures else 0


def read_workflows() -> dict[str, str]:
    sources = {}
    for fn in sorted(os.listdir(WORKFLOW_DIR)):
        if fn.endswith((".yml", ".yaml")):
            with open(os.path.join(WORKFLOW_DIR, fn), encoding="utf-8") as fh:
                sources[fn] = fh.read()
    return sources


def fetch_runs(workflow: str) -> list[dict]:
    out = subprocess.run(
        ["gh", "api", f"repos/{REPO}/actions/workflows/{workflow}/runs?per_page=100"],
        capture_output=True,
        text=True,
    )
    if out.returncode != 0:
        print(f"check-release-workflows: cannot read runs for {workflow}")
        print(f"  {out.stderr.strip()}")
        return []
    return json.loads(out.stdout).get("workflow_runs", [])


def main() -> int:
    if "--selftest" in sys.argv:
        return selftest()

    args = [a for a in sys.argv[1:] if not a.startswith("-")]
    if len(args) != 1:
        print(__doc__)
        return 2
    tag = args[0]

    workflows = tag_triggered_workflows(read_workflows(), tag)
    if not workflows:
        print(f"check-release-workflows: no workflow claims to fire on {tag}")
        return 1

    bad = 0
    for workflow in workflows:
        status, detail = assess_runs(fetch_runs(workflow), tag)
        print(f"  {status:<8} {workflow:<22} {detail}")
        if status != OK:
            bad += 1

    if bad:
        print()
        print(f"check-release-workflows: {bad} of {len(workflows)} did not run green against {tag}.")
        print("  A tag whose workflows never fired looks identical to one whose")
        print("  workflows all passed. Check the push that created the tag: a")
        print("  large multi-ref push raises no push event at all (#1412).")
        return 1

    print(f"check-release-workflows: ok ({len(workflows)} workflow(s) ran green against {tag})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
