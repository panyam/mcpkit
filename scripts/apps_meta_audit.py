#!/usr/bin/env python3
"""One-shot parity audit across every compat fixture.

For each fixture in `_apps_common.FIXTURES`:
  1. Build the mcpkit-Go fixture binary.
  2. Build the upstream TS reference example.
  3. Start the mcpkit fixture on :3101.
  4. Start the upstream TS server on :3102.
  5. Run `scripts/apps_parity_diff.py` against both.
  6. Capture verdict (pass / drift / error).
  7. Kill both servers; move on.

Writes `conformance/RESOURCES_META_AUDIT.md` summarizing per-fixture
results. The same parity diff script is wired into the DOCKER-mode
Playwright wrapper as a CI gate (see scripts/apps-playwright-docker-inner.sh);
this audit is the one-shot equivalent run against all fixtures sequentially.

Why the audit exists: the original tools/list-only drift check missed
both the transcript-permissions bug (PR 623) and the sheet-music-CSP
bug (PR 643) because both lived on `resources/read` _meta, not
`tools/list`. The extended diff catches these going forward; this
script surfaces any that are sitting latent today.

Usage:
  uv run scripts/apps_meta_audit.py [--out conformance/RESOURCES_META_AUDIT.md] [--example basic-vanillajs]

Stdlib only (apart from _apps_common.py).
"""
from __future__ import annotations

import argparse
import subprocess
import sys
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Optional

from _apps_common import (
    DEFAULT_EXT_APPS_DIR,
    DEFAULT_FIXTURE_PORT,
    FIXTURES,
    FIXTURES_BY_NAME,
    Fixture,
    MCPKIT_ROOT,
    build_go_fixture,
    build_upstream_example,
    cleanup_proc,
    die,
    ensure_ext_apps_clone,
    info,
    install_upstream_deps,
    kill_port,
    tail_file,
    wait_for_fixture,
)
from apps_demo import start_upstream_ts_server


PARITY_DIFF_SCRIPT = MCPKIT_ROOT / "scripts" / "apps_parity_diff.py"
DEFAULT_OUT = MCPKIT_ROOT / "conformance" / "RESOURCES_META_AUDIT.md"
UPSTREAM_PORT = 3102


@dataclass
class FixtureResult:
    fixture: Fixture
    status: str  # "pass", "drift", "error"
    summary: str  # one-liner for the table
    detail: str  # full diff or error tail, embedded in details/summary block


def _run_fixture(fixture: Fixture, ext_apps_dir: Path) -> FixtureResult:
    """Build + start the mcpkit+upstream pair for one fixture, run the
    parity diff, capture verdict, tear down."""
    info("")
    info(f"=== {fixture.name} ===")
    info(f"    fixture_dir:      {fixture.fixture_dir}")
    info(f"    upstream_example: {fixture.upstream_example}")

    fixture_dir = MCPKIT_ROOT / fixture.fixture_dir
    fixture_bin = Path(f"/tmp/mcpkit-audit-{fixture.name}")
    mcpkit_log = Path(f"/tmp/mcpkit-audit-{fixture.name}.log")
    upstream_log = Path(f"/tmp/upstream-audit-{fixture.name}.log")

    mcpkit_proc: Optional[subprocess.Popen] = None
    upstream_proc: Optional[subprocess.Popen] = None

    try:
        # Build both sides.
        try:
            build_go_fixture(fixture_dir, fixture_bin)
        except subprocess.CalledProcessError as exc:
            return FixtureResult(
                fixture, "error", "go build failed",
                f"go build failed for {fixture.fixture_dir}:\n{exc}",
            )
        try:
            build_upstream_example(ext_apps_dir, fixture.upstream_example)
        except subprocess.CalledProcessError as exc:
            return FixtureResult(
                fixture, "error", "upstream build failed",
                f"upstream build failed for {fixture.upstream_example}:\n{exc}",
            )

        # Start mcpkit fixture on :3101.
        kill_port(DEFAULT_FIXTURE_PORT)
        env_mcpkit = {
            "EXT_APPS_DIR": str(ext_apps_dir),
            "PORT": str(DEFAULT_FIXTURE_PORT),
        }
        mcpkit_proc = subprocess.Popen(
            [str(fixture_bin)],
            stdout=mcpkit_log.open("w"),
            stderr=subprocess.STDOUT,
            env={**__import__("os").environ, **env_mcpkit},
        )
        if not wait_for_fixture(DEFAULT_FIXTURE_PORT, timeout_s=15):
            tail = _read_tail(mcpkit_log, 15)
            return FixtureResult(
                fixture, "error", "mcpkit fixture did not start",
                f"mcpkit fixture did not respond on :{DEFAULT_FIXTURE_PORT}\n{tail}",
            )

        # Start upstream on :3102. pdf-server needs --enable-interact.
        kill_port(UPSTREAM_PORT)
        upstream_proc = start_upstream_ts_server(
            ext_apps_dir, fixture.upstream_example, UPSTREAM_PORT, upstream_log,
        )
        # pdf-server requires --enable-interact to expose the 9-tool surface;
        # start_upstream_ts_server doesn't pass it (caller's responsibility).
        # If we hit this, kill + restart with the flag.
        if fixture.name == "pdf-server":
            cleanup_proc(upstream_proc)
            kill_port(UPSTREAM_PORT)
            upstream_proc = _start_upstream_with_flag(
                ext_apps_dir, fixture.upstream_example, UPSTREAM_PORT,
                upstream_log, "--enable-interact",
            )
        if not wait_for_fixture(UPSTREAM_PORT, timeout_s=20):
            tail = _read_tail(upstream_log, 15)
            return FixtureResult(
                fixture, "error", "upstream did not start",
                f"upstream TS server did not respond on :{UPSTREAM_PORT}\n{tail}",
            )

        # Run the parity diff. Capture stderr so we can quote it in the
        # report if drift is found.
        proc = subprocess.run(
            [
                "python3", str(PARITY_DIFF_SCRIPT),
                "mcpkit", f"http://localhost:{DEFAULT_FIXTURE_PORT}/mcp",
                "upstream", f"http://localhost:{UPSTREAM_PORT}/mcp",
            ],
            capture_output=True,
            text=True,
            timeout=60,
        )
        if proc.returncode == 0:
            summary = proc.stdout.strip() or "parity OK"
            return FixtureResult(fixture, "pass", summary, "")
        # Drift. Combine stderr for the detail block.
        detail = proc.stderr.strip() or proc.stdout.strip() or "no output"
        # Summary line: first drift-surface line.
        first_drift = ""
        for line in detail.splitlines():
            if line.startswith("DRIFT in "):
                first_drift = line
                break
        summary = first_drift or "drift detected"
        return FixtureResult(fixture, "drift", summary, detail)

    except Exception as exc:
        return FixtureResult(
            fixture, "error", f"audit raised: {type(exc).__name__}", str(exc),
        )

    finally:
        cleanup_proc(mcpkit_proc)
        cleanup_proc(upstream_proc)
        kill_port(DEFAULT_FIXTURE_PORT)
        kill_port(UPSTREAM_PORT)
        # Give ports a moment to release.
        time.sleep(0.5)


def _start_upstream_with_flag(
    ext_apps_dir: Path, example: str, port: int, log_file: Path, *extra: str,
) -> subprocess.Popen:
    """Variant of apps_demo.start_upstream_ts_server with extra CLI flags."""
    example_dir = ext_apps_dir / "examples" / example
    if (example_dir / "dist" / "index.js").exists():
        cmd = ["node", "dist/index.js", *extra]
    elif (example_dir / "main.ts").exists():
        cmd = ["npx", "tsx", "main.ts", *extra]
    else:
        die(f"don't know how to start {example} — no dist/index.js or main.ts")
    import os as _os

    env = _os.environ.copy()
    env["PORT"] = str(port)
    return subprocess.Popen(
        cmd,
        cwd=example_dir,
        stdout=log_file.open("w"),
        stderr=subprocess.STDOUT,
        env=env,
    )


def _read_tail(path: Path, n: int) -> str:
    """Return the last n lines of a log file as a code-fenced string for
    embedding in Markdown."""
    try:
        lines = path.read_text().splitlines()
        if len(lines) > n:
            lines = lines[-n:]
        return "```\n" + "\n".join(lines) + "\n```"
    except OSError:
        return "_(log unavailable)_"


def _is_systematic_only(detail: str) -> bool:
    """Recognize the two systematic mcpkit-vs-upstream convention diffs
    that fire on every fixture and have no fixture-specific signal:

      1. `prompts: { listChanged: true }` — mcpkit's server framework
         always advertises the prompts capability; upstream advertises
         it only when the fixture registers a prompt. A fixture that
         registers no prompts shows this as drift in initialize.

      2. resources/list `name` field — mcpkit auto-derives `"<tool> UI"`
         from `cfg.Name + " UI"`; upstream uses the URI as the name.
         Different conventions; neither wrong; fires on every fixture.

      3. resources/list `description` field — upstream's fixtures
         declare one in the registration call; mcpkit's RegisterAppTool
         doesn't expose a Description field for the resource def, so
         mcpkit omits it. Filed as a follow-up.

    A drift block that contains ONLY these patterns gets classified as
    "systematic" — the audit's actionable findings are everything else.
    """
    # Strip the DRIFT line headers and the empty unified-diff frame
    # lines to look at just the +/- content.
    add_remove_lines = [
        line for line in detail.splitlines()
        if (line.startswith("+") or line.startswith("-"))
        and not line.startswith("+++")
        and not line.startswith("---")
    ]
    if not add_remove_lines:
        return False
    # Lines carrying real content. JSON-pretty-printed diffs include
    # punctuation-only closing-brace lines (`+    },`, `-    }`) that
    # belong to whichever block was added/removed; they don't carry their
    # own signal. Skip them when classifying.
    content_lines = [
        line for line in add_remove_lines
        if line.strip("+- \t").rstrip(",}{[ \t") != ""
    ]
    if not content_lines:
        return False
    systematic_markers = (
        '"prompts"',
        '"listChanged"',
        '"name":',
        '"description":',
    )
    return all(
        any(marker in line for marker in systematic_markers)
        for line in content_lines
    )


def _render_report(results: list[FixtureResult]) -> str:
    n_pass = sum(1 for r in results if r.status == "pass")
    n_error = sum(1 for r in results if r.status == "error")
    drift_results = [r for r in results if r.status == "drift"]

    # Split drift into systematic-only vs actionable.
    systematic = [r for r in drift_results if _is_systematic_only(r.detail)]
    actionable = [r for r in drift_results if not _is_systematic_only(r.detail)]

    lines = []
    lines.append("# apps/compat fixture parity audit")
    lines.append("")
    lines.append(
        "Per-fixture wire-surface parity between the mcpkit-Go drop-in and "
        "upstream's TypeScript reference server. Generated by "
        "`scripts/apps_meta_audit.py`; runs the same `apps_parity_diff.py` "
        "that gates DOCKER-mode CI in `apps-playwright-docker-inner.sh`."
    )
    lines.append("")
    lines.append("Surfaces compared per fixture:")
    lines.append("")
    lines.append("1. `initialize` — `serverInfo` + fixture-relevant `capabilities`")
    lines.append("2. `tools/list` — every tool's name, title, description, schemas, tool `_meta`")
    lines.append("3. `resources/list` — registered resource URIs + def `_meta`")
    lines.append("4. `resources/templates/list` — URI templates")
    lines.append("5. `resources/read` — per-content `_meta` (where `csp` + `permissions` live)")
    lines.append("")
    lines.append("Iframe HTML body (`text`/`blob`) is intentionally NOT compared — different bundlers produce different bytes for the same logical App.")
    lines.append("")
    lines.append(
        f"**Summary:** {n_pass} pass · {len(actionable)} actionable drift · "
        f"{len(systematic)} systematic-only drift · {n_error} error · "
        f"{len(results)} total."
    )
    lines.append("")
    lines.append("**Actionable drift** = the audit found a fixture-specific divergence (missing `_meta`, wrong URI, etc.) that's worth fixing. **Systematic-only drift** = mcpkit-framework-wide convention differences that fire on every fixture and aren't fixture bugs — listed in the *Known systematic differences* section below.")
    lines.append("")
    lines.append("| Fixture | Status | Notes |")
    lines.append("|---|---|---|")
    for r in results:
        if r.status == "pass":
            label = "✓ pass"
        elif r.status == "error":
            label = "✗ error"
        elif r in actionable:
            label = "⚠ drift — actionable"
        else:
            label = "○ drift — systematic only"
        notes = r.summary
        if len(notes) > 120:
            notes = notes[:117] + "…"
        notes = notes.replace("\n", " ").replace("|", "\\|")
        lines.append(f"| `{r.fixture.name}` | {label} | {notes} |")
    lines.append("")

    # Known systematic section — explain the noise once so 19 ○ rows above
    # are interpretable without scrolling 21 details blocks.
    lines.append("## Known systematic differences")
    lines.append("")
    lines.append("These appear on every (or nearly every) fixture and are mcpkit-framework conventions vs upstream's per-fixture conventions, not fixture bugs:")
    lines.append("")
    lines.append("- **`capabilities.prompts.listChanged`** — mcpkit's server framework advertises the prompts capability on every server. Upstream advertises it only when the fixture registers a prompt. None of the compat fixtures register prompts, so all 21 show this in the initialize diff. Server-framework follow-up.")
    lines.append("- **`resources/list` — `name`** — mcpkit auto-derives `\"<tool> UI\"` from the AppToolConfig's `cfg.Name`. Upstream uses the URI as the name. Different conventions; neither is wrong; both are spec-compliant.")
    lines.append("- **`resources/list` — `description`** — upstream's fixtures pass a description through `registerAppResource`; mcpkit's `AppToolConfig` doesn't surface a `Description` field for the resource def, so mcpkit omits it. Add the field to make per-fixture descriptions reachable.")
    lines.append("")

    if actionable:
        lines.append("## Actionable findings")
        lines.append("")
        lines.append("Each one is a fixture-specific divergence — typically a missing `_meta` block — that's worth fixing. Same pattern as transcript permissions (PR 623) + sheet-music CSP (PR 643).")
        lines.append("")
        for r in actionable:
            lines.append(f"### `{r.fixture.name}` — {r.summary}")
            lines.append("")
            lines.append("<details><summary>Full diff</summary>")
            lines.append("")
            lines.append("```")
            lines.append(r.detail.strip())
            lines.append("```")
            lines.append("")
            lines.append("</details>")
            lines.append("")

    if systematic:
        lines.append("## Systematic-only details")
        lines.append("")
        lines.append("Folded into one details block per fixture — each is dominated by the *Known systematic differences* above.")
        lines.append("")
        for r in systematic:
            lines.append(f"<details><summary><code>{r.fixture.name}</code> diff</summary>")
            lines.append("")
            lines.append("```")
            lines.append(r.detail.strip())
            lines.append("```")
            lines.append("")
            lines.append("</details>")
            lines.append("")

    error_results = [r for r in results if r.status == "error"]
    if error_results:
        lines.append("## Errors")
        lines.append("")
        for r in error_results:
            lines.append(f"### `{r.fixture.name}`")
            lines.append("")
            lines.append("```")
            lines.append(r.detail.strip())
            lines.append("```")
            lines.append("")

    if not drift_results and not error_results:
        lines.append("No findings — every fixture matches upstream.")
        lines.append("")
    return "\n".join(lines) + "\n"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    parser.add_argument(
        "--out",
        type=Path,
        default=DEFAULT_OUT,
        help=f"Markdown report path (default: {DEFAULT_OUT.relative_to(MCPKIT_ROOT)})",
    )
    parser.add_argument(
        "--example",
        default=None,
        help="Restrict the audit to a single fixture (debugging).",
    )
    parser.add_argument(
        "--ext-apps-dir",
        type=Path,
        default=Path(DEFAULT_EXT_APPS_DIR),
        help="ext-apps checkout (default: /tmp/ext-apps; cloned if absent)",
    )
    args = parser.parse_args()

    if not PARITY_DIFF_SCRIPT.exists():
        die(f"parity diff script not found at {PARITY_DIFF_SCRIPT}")

    fixtures: list[Fixture]
    if args.example:
        if args.example not in FIXTURES_BY_NAME:
            die(
                f"unknown fixture: {args.example}\n"
                f"valid: {', '.join(f.name for f in FIXTURES)}"
            )
        fixtures = [FIXTURES_BY_NAME[args.example]]
    else:
        fixtures = list(FIXTURES)

    ensure_ext_apps_clone(args.ext_apps_dir)
    install_upstream_deps(args.ext_apps_dir)

    info("")
    info(f"Auditing {len(fixtures)} fixture(s)...")

    results = []
    for f in fixtures:
        results.append(_run_fixture(f, args.ext_apps_dir))

    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(_render_report(results))
    info("")
    try:
        out_display = args.out.relative_to(MCPKIT_ROOT)
    except ValueError:
        out_display = args.out
    info(f"Report written: {out_display}")

    n_pass = sum(1 for r in results if r.status == "pass")
    n_drift = sum(1 for r in results if r.status == "drift")
    n_error = sum(1 for r in results if r.status == "error")
    info(f"  {n_pass} pass · {n_drift} drift · {n_error} error")

    return 0 if (n_drift == 0 and n_error == 0) else 1


if __name__ == "__main__":
    sys.exit(main())
