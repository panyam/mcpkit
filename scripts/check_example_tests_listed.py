#!/usr/bin/env python3
"""Fail when an example module's tests run nowhere in CI (#1431).

`make test-examples` runs an explicit list in examples/Makefile. A module with
tests that is missing from the list ships with those tests unrun, and a list
entry whose module has no tests runs nothing while looking covered. Both
happened: whole-enchilada/events/tools/gen-compose was never listed, and
tasks-v2 stayed listed after its tests left with the agent SDK.

A module counts as run when `make -C examples test` names its directory,
either directly or in a script one of its recipes calls, or when a test.yml job
uses it as a working-directory. examples/justfile must name the same set
(examples/CONVENTIONS.md §6).
"""
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EXAMPLES = os.path.join(ROOT, "examples")
SKIP_DIRS = {"node_modules", ".git", "testdata", "vendor"}


def module_dirs_with_tests():
    """Map each example module (relative to examples/) to whether it owns a _test.go."""
    modules = {}
    for dirpath, dirnames, filenames in os.walk(EXAMPLES):
        dirnames[:] = sorted(d for d in dirnames if d not in SKIP_DIRS)
        if "go.mod" in filenames:
            modules[os.path.relpath(dirpath, EXAMPLES)] = False
    for dirpath, dirnames, filenames in os.walk(EXAMPLES):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        if not any(f.endswith("_test.go") for f in filenames):
            continue
        # A test file belongs to its nearest enclosing module.
        rel = os.path.relpath(dirpath, EXAMPLES)
        while rel not in modules and rel not in ("", "."):
            rel = os.path.dirname(rel)
        if rel in modules:
            modules[rel] = True
    return modules


def read(path):
    with open(path) as f:
        return f.read()


def named_dirs(text, prefixes):
    """Every examples-relative path that follows one of prefixes in text."""
    out = set()
    for prefix in prefixes:
        for m in re.finditer(re.escape(prefix) + r"([A-Za-z0-9_./-]+)", text):
            out.add(m.group(1).rstrip("/"))
    return out


def runner_dirs(text, prefixes, modules):
    """Example modules a recipe text names, following the scripts it calls."""
    dirs = named_dirs(text, prefixes)
    for script in re.findall(r"experimental/scripts/[A-Za-z0-9_.-]+\.sh", text):
        dirs |= named_dirs(read(os.path.join(ROOT, script)), ["examples/"])
    return dirs & set(modules)


def make_test_commands():
    """The commands `make -C examples test` would run, so a target outside the
    umbrella does not count as run."""
    return subprocess.run(
        ["make", "-s", "-n", "-C", EXAMPLES, "test"],
        check=True, capture_output=True, text=True,
    ).stdout


def just_test_recipes():
    """The bodies of the recipes the justfile's `test` depends on (no `just` on
    CI runners, so this reads the file rather than dry-running it)."""
    text = read(os.path.join(EXAMPLES, "justfile"))
    deps = re.search(r"^test:(.*)$", text, re.M).group(1).split()
    bodies = []
    for dep in deps:
        m = re.search(r"^" + re.escape(dep) + r":.*\n((?:[ \t]+.*\n?)*)", text, re.M)
        if m:
            bodies.append(m.group(1))
    return "".join(bodies)


def main():
    modules = module_dirs_with_tests()
    make_dirs = runner_dirs(make_test_commands(), [EXAMPLES + "/"], modules)
    just_dirs = runner_dirs(just_test_recipes(), ["{{EXAMPLES_DIR}}/"], modules)
    job_dirs = modules.keys() & set(
        re.findall(
            r"working-directory:\s*examples/([A-Za-z0-9_./-]+?)/?\s*$",
            read(os.path.join(ROOT, ".github", "workflows", "test.yml")),
            re.M,
        )
    )

    errors = []
    for mod, has_tests in sorted(modules.items()):
        if has_tests and mod not in make_dirs and mod not in job_dirs:
            errors.append(
                f"examples/{mod} has tests that nothing runs: add a target to "
                "examples/Makefile's `test` umbrella (and the justfile twin)"
            )
    for mod in sorted(make_dirs):
        if mod in modules and not modules[mod]:
            errors.append(
                f"examples/Makefile runs examples/{mod}, which has no _test.go files, "
                "so its target passes without testing anything"
            )
    for mod in sorted(make_dirs ^ just_dirs):
        where = "examples/Makefile" if mod in make_dirs else "examples/justfile"
        errors.append(f"examples/{mod} is run by {where} only; the two must match")

    if errors:
        for e in errors:
            print("FAIL: " + e)
        return 1
    tested = sum(modules.values())
    print(
        f"check-example-tests-listed: {tested} example modules have tests, "
        f"{len(make_dirs)} run by test-examples, {len(job_dirs)} by their own CI job."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
