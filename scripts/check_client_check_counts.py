#!/usr/bin/env python3
"""Guard against silently-thinning conformance scenarios.

A scenario that fails at setup emits only a shadow of its real check
surface. The SEP-2352 migration scenario spent months reporting 3 countable
checks where 31 belong, because the client died at the wire handshake before
the OAuth flow started (fixed in PR 1113). Scenario-level pass/fail cannot
see this, but the emitted-check COUNT can.

Denominators differ, so be explicit about which one is in play. That
scenario writes 79 entries to checks.json once healthy, of which 31 are
countable in the "Passed: N/M" summary and 16 are unique substantive check
IDs. The rest are incoming-request / outgoing-response INFO rows.

This script snapshots len(checks), the raw entry count, because it needs a
single number that moves monotonically with depth and costs nothing to
compute. That denominator includes the INFO rows, so it partly tracks HTTP
traffic volume and can shift when request patterns change with no loss of
coverage. Treat a drop as a signal to look, not as proof of a regression.
Conformance issue 451 proposes an upstream per-scenario expected-vs-emitted
signal keyed on unique check IDs, which would replace this guard outright.

This script compares each PASSING scenario's emitted-check count in a
fresh run artifact against the committed snapshot
(conformance/client-check-counts.json):

  - count dropped        -> FAIL (possible thinning regression)
  - count grew / new     -> warn + suggest refreshing the snapshot
  - scenario had FAILUREs -> skipped (counts are volatile mid-failure)

Refresh the snapshot after a deliberate change (upstream added checks,
client exercises deeper paths):

  CONF_CLIENT_UPDATE_COUNTS=1 make testconf-client

Usage: check_client_check_counts.py <artifact-dir> [--update]
"""
import glob
import json
import os
import re
import sys

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SNAPSHOT = os.path.join(REPO_ROOT, "conformance", "client-check-counts.json")
TS_SUFFIX = re.compile(r"-\d{4}-\d{2}-\d{2}T[\d-]+Z$")


def collect(artifact_dir):
    scenarios = {}
    for f in glob.glob(os.path.join(artifact_dir, "**", "checks.json"), recursive=True):
        rel = os.path.relpath(os.path.dirname(f), artifact_dir)
        name = TS_SUFFIX.sub("", rel.replace(os.sep, "/"))
        checks = json.load(open(f))
        failed = sum(1 for c in checks if c.get("status") == "FAILURE")
        scenarios[name] = {"total": len(checks), "failed": failed}
    return scenarios


def main():
    args = [a for a in sys.argv[1:] if a != "--update"]
    update = "--update" in sys.argv[1:] or os.environ.get("CONF_CLIENT_UPDATE_COUNTS") == "1"
    if len(args) != 1:
        print(__doc__)
        return 2
    scenarios = collect(args[0])
    if not scenarios:
        print(f"check-counts: no checks.json found under {args[0]}")
        return 2

    passing = {n: s["total"] for n, s in sorted(scenarios.items()) if s["failed"] == 0}

    if update or not os.path.exists(SNAPSHOT):
        with open(SNAPSHOT, "w") as f:
            json.dump(passing, f, indent=2, sort_keys=True)
            f.write("\n")
        print(f"check-counts: snapshot written ({len(passing)} passing scenarios) -> {SNAPSHOT}")
        return 0

    snapshot = json.load(open(SNAPSHOT))
    drops, grows, new = [], [], []
    for name, count in passing.items():
        prev = snapshot.get(name)
        if prev is None:
            new.append((name, count))
        elif count < prev:
            drops.append((name, prev, count))
        elif count > prev:
            grows.append((name, prev, count))

    for name, prev, count in drops:
        print(f"check-counts: DROP {name}: {prev} -> {count} emitted checks")
    for name, prev, count in grows:
        print(f"check-counts: note {name}: {prev} -> {count} (coverage grew; refresh snapshot)")
    for name, count in new:
        print(f"check-counts: note new scenario {name}: {count} checks (refresh snapshot)")

    if drops:
        print("")
        print("A passing scenario emitted fewer checks than the committed snapshot.")
        print("This is the thin-shadow signature (setup failure masking depth, see")
        print("scripts/check_client_check_counts.py docstring). Investigate before")
        print("refreshing: CONF_CLIENT_UPDATE_COUNTS=1 make testconf-client")
        return 1
    print(f"check-counts: {len(passing)} passing scenarios at or above snapshot depth.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
