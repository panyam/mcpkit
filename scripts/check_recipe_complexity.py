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

# Make function calls and just interpolations are expansions, not shell. They
# are masked before the control-flow scan because `$(if $(EVERY),--every
# $(EVERY))` is one optional argument on one line, and reading its `if` as
# control flow flagged `drive-chat` -- a single `go run` -- while leaving the
# justfile's identical `{{if EVERY != '' { ... } }}` alone. Same recipe, same
# semantics, flagged in one dialect only: the gate was wrong, not the recipe.
# In a Makefile, `$(...)` is a Make expansion and `$$(...)` is shell command
# substitution. In a justfile, `{{...}}` is the expansion and `$(...)` is shell.
# The distinction decides what gets scanned: `@echo "... $(if $(BUILD), ...)"` in
# a Makefile is a banner, while `echo "$(for f in $PAGES; do ...)"` in a
# justfile is a loop wearing an echo.
JUST_EXPANSION_RE = re.compile(r'\{\{.*?\}\}')
MAKE_EXPANSION_RE = re.compile(r'\{\{.*?\}\}|(?<!\$)\$\((?:[^()]|\([^()]*\))*\)')


def mask_expansions(stmt: str, is_make: bool) -> str:
    """Blank out template expansions so only real shell is left to scan."""
    pattern = MAKE_EXPANSION_RE if is_make else JUST_EXPANSION_RE
    prev = None
    while prev != stmt:
        prev, stmt = stmt, pattern.sub(" ", stmt)
    return stmt
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


def assess(body: list[str], is_make: bool = True) -> tuple[bool, int, str]:
    """Return (violates, statement_count, reason)."""
    statements, continued = 0, False
    for line in body:
        stmt = strip_prefix(line)
        if SHEBANG_RE.match(line) or stmt in BOILERPLATE:
            continue
        if stmt.startswith("#"):
            continue
        # Every physical line, continued or not: the logic is in there either way.
        m = CONTROL_RE.search(mask_expansions(stmt, is_make))
        if m:
            return True, statements, f"control flow (`{m.group(1)}`)"
        if not continued and not ECHO_RE.match(stmt):
            statements += 1
        continued = stmt.endswith("\\")
    if statements >= MAX_STATEMENTS:
        return True, statements, f"{statements} statements"
    return False, statements, ""


def is_makefile(rel: str) -> bool:
    return os.path.basename(rel) == "Makefile" or rel.endswith(".mk")


def walk():
    for dirpath, dirnames, filenames in os.walk(ROOT):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        for fn in sorted(filenames):
            if fn in NAMES or fn.endswith(SUFFIXES):
                yield os.path.join(dirpath, fn)


# Cases that pinned down the rule. Every one was a bug in this checker first,
# in both directions, so they are kept as a test.
#
# The fixtures are inline rather than pointed at real recipes. The first version
# named live ones, and the extraction sweep they exist to support promptly fixed
# two of them, turning the self-test red for the best possible reason. A test
# whose fixture is the thing being changed measures the change, not the rule.
#
# (label, is_make, body lines, should_violate)
SELFTEST = [
    (
        'make: continued for/if loop (vulncheck shape)',
        True,
        [
            '\tfailed=""; \\',
            '\techo "==> govulncheck root"; \\',
            '\tfor mod in $(SUB_MODS); do \\',
            '\t\tif [ -f "$$mod/go.mod" ]; then \\',
            '\t\t\techo "$$mod"; \\',
            '\t\tfi; \\',
            '\tdone',
        ],
        True,
    ),
    (
        'make: bare for loop (clear_all_tokens shape)',
        True,
        [
            '\t@for realm in asgard babylon camelot; do \\',
            '\t\tkcadm create logout-all -r $$realm; \\',
            '\tdone',
        ],
        True,
    ),
    (
        "just: for inside an echo's $() is shell, not an expansion",
        False,
        [
            '\tPAGES=$(ls *.md)',
            '\techo "written: $(for f in $PAGES; do grep -L STUB "$f"; done | wc -l)"',
        ],
        True,
    ),
    (
        'make: $(if ...) in a banner is a Make function',
        True,
        [
            '\t$(COMPOSE) up -d --wait $(if $(BUILD),--build)',
            '\t@echo "stack up$(if $(BUILD), (rebuilt)); nginx on http://localhost:9090"',
            '\t@echo "verify with: make smoke"',
        ],
        False,
    ),
    (
        'make: one go run with an optional flag (drive-chat shape)',
        True,
        [
            '\tgo -C drivers/synth run . --event chat.message $(if $(EVERY),--every $(EVERY))',
        ],
        False,
    ),
    (
        'make: one command plus an echo banner (docker/backends up shape)',
        True,
        [
            '\t$(COMPOSE) up -d',
            '\t@echo ""',
            '\t@echo "  Keycloak UI: http://localhost:8180"',
            '\t@echo "  Postgres:    postgres:5432"',
            '\t@echo "  Redis:       redis:6379"',
            '\t@echo ""',
        ],
        False,
    ),
    (
        'just: a two-line shebang recipe is not a script',
        False,
        [
            '\t#!/usr/bin/env bash',
            '\tset -eu',
            '\tdocker compose -f db.yaml down -v',
        ],
        False,
    ),
    (
        'make: four plain statements crosses the line',
        True,
        [
            '\tcd a && go build ./...',
            '\tcd b && go build ./...',
            '\tcd c && go build ./...',
            '\tcd d && go build ./...',
        ],
        True,
    ),
]


def selftest() -> int:
    failures = 0
    for label, is_make, body, expect in SELFTEST:
        got, _, reason = assess(body, is_make=is_make)
        if got != expect:
            print(f"selftest: {label}")
            print(f"  expected violates={expect}, got {got} ({reason or 'clean'})")
            failures += 1
    print(f"selftest: {len(SELFTEST) - failures}/{len(SELFTEST)} cases pass")
    return 1 if failures else 0


def main() -> int:
    if "--selftest" in sys.argv:
        return selftest()
    listing = "--list" in sys.argv
    allowed = load_allowed()
    found, violations = {}, []

    for path in walk():
        rel = os.path.relpath(path, ROOT)
        for recipe, body in parse(path):
            violates, count, reason = assess(body, is_make=is_makefile(rel))
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
