#!/usr/bin/env python3
"""C8 gate — Makefiles and justfiles dispatch; they do not hold shell scripts.

A recipe with control flow or a pile of statements has to be written twice, once
in Make's escaping dialect and once in just's, and the two copies drift. That is
not hypothetical: `just clean-backends` in examples/whole-enchilada/events ended
up byte-identical to `just clean`, wiping the events volumes while its own doc
comment promised to wipe docker/backends, because the Makefile carried the real
logic and the justfile copy rotted (#1396).

A recipe is non-compliant when it uses control flow (if / for / while / case /
read / until) or runs 4 or more separate statements. Line continuations count as
one statement, so a single long command is fine, and `echo` banners do not count
at all. A just shebang recipe is judged by its body, not by having a shebang.

ALLOWED lists what predates the rule. Shrinking it is the point; an entry that
no longer violates is an error, not a pass, so the list cannot rot the way a
--update-baseline flag lets a baseline rot.

Usage:
    scripts/check_recipe_complexity.py            # gate
    scripts/check_recipe_complexity.py --list     # print offenders, ALLOWED format
"""
from __future__ import annotations

import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SKIP_DIRS = {".venv", ".git", "node_modules", "data", "dist", "build"}
NAMES = {"Makefile", "justfile"}
SUFFIXES = (".just", ".mk")

MAX_STATEMENTS = 4

# A recipe header: `name:` or `name arg:` or `name: deps`. Not `X := val`, not
# `.PHONY:`, not an indented line.
RECIPE_RE = re.compile(r'^([A-Za-z0-9_][A-Za-z0-9_.-]*)[^:=]*:(?!=)')

# Control flow at the head of a statement, checked on every physical line. A
# Make recipe is routinely one backslash-continued shell program, so looking
# only at statement-initial lines misses `vulncheck`-shaped recipes entirely --
# a 16-line for/if loop that is technically one statement.
#
# Anchored so that `psql -c "... read ..."`, a target called `if-changed`, or
# the word "for" mid-sentence in an echo does not trip it.
CONTROL_RE = re.compile(
    r'(?:^|[;&|(]\s*|\bthen\s+|\bdo\s+|\belse\s+)(if|for|while|case|read|until)\s'
)

# Output, not logic. A recipe that runs one command and then echoes a six-line
# banner is still dispatch, and counting those lines made the gate fire on
# docker/backends' `up`.
ECHO_RE = re.compile(r'^@?\s*(echo|@echo)\b')
SHEBANG_RE = re.compile(r'^\s*#!')

# Entries are "<path relative to repo root>::<recipe>", one per line.
ALLOWED_PATH = os.path.join(ROOT, "scripts", "recipe-complexity-allowed.txt")


def load_allowed() -> set[str]:
    if not os.path.exists(ALLOWED_PATH):
        return set()
    out = set()
    with open(ALLOWED_PATH, encoding="utf-8") as fh:
        for line in fh:
            line = line.split("#", 1)[0].strip()
            if line:
                out.add(line)
    return out


def strip_prefix(stmt: str) -> str:
    """Drop Make's @ and - recipe prefixes before looking for control flow."""
    return stmt.lstrip().lstrip("@-").lstrip()


def parse(path: str):
    """Yield (recipe_name, [body lines]) for one Makefile or justfile."""
    name, body = None, []
    with open(path, encoding="utf-8", errors="replace") as fh:
        for raw in fh:
            line = raw.rstrip("\n")
            if line.startswith(("\t", "    ")) and name is not None:
                if line.strip():
                    body.append(line)
                continue
            if name is not None:
                yield name, body
            m = RECIPE_RE.match(line)
            name, body = (m.group(1) if m else None), []
    if name is not None:
        yield name, body


# just runs a multi-line bash body by giving the recipe a shebang, so the
# shebang itself says nothing about complexity -- `downdb` is two lines. Judge
# the body, and ignore the boilerplate that carries no logic.
BOILERPLATE = {"set -eu", "set -e", "set -euo pipefail", "set -o pipefail"}


def assess(body: list[str]) -> tuple[bool, int, str]:
    """Return (violates, statement_count, reason)."""
    statements, continued = 0, False
    for line in body:
        stmt = strip_prefix(line)
        if SHEBANG_RE.match(line) or stmt in BOILERPLATE:
            continue
        if stmt.startswith("#"):
            continue
        # Every physical line, continued or not: the logic is in there either way.
        m = CONTROL_RE.search(stmt)
        if m:
            return True, statements, f"control flow (`{m.group(1)}`)"
        if not continued and not ECHO_RE.match(stmt):
            statements += 1
        continued = stmt.endswith("\\")
    if statements >= MAX_STATEMENTS:
        return True, statements, f"{statements} statements"
    return False, statements, ""


def walk():
    for dirpath, dirnames, filenames in os.walk(ROOT):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        for fn in sorted(filenames):
            if fn in NAMES or fn.endswith(SUFFIXES):
                yield os.path.join(dirpath, fn)


def main() -> int:
    listing = "--list" in sys.argv
    allowed = load_allowed()
    found, violations = {}, []

    for path in walk():
        rel = os.path.relpath(path, ROOT)
        for recipe, body in parse(path):
            violates, count, reason = assess(body)
            if not violates:
                continue
            key = f"{rel}::{recipe}"
            found[key] = reason
            if key not in allowed:
                violations.append((key, reason))

    if listing:
        for key in sorted(found):
            print(key)
        return 0

    stale = sorted(allowed - set(found))
    for key, reason in sorted(violations):
        print(f"check-recipe-complexity: {key} — {reason}")
        print("  Move it to a script and have the recipe call that. Both runners")
        print("  can then share one implementation instead of drifting.")
    for key in stale:
        print(f"check-recipe-complexity: {key} is in {os.path.basename(ALLOWED_PATH)} but no longer violates.")
        print("  Delete the line. A baseline that keeps entries it does not need")
        print("  stops telling you anything.")

    if violations or stale:
        return 1
    print(f"check-recipe-complexity: ok ({len(allowed)} recipe(s) knowingly pre-C8)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
