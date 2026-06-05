#!/usr/bin/env python3
"""Run upstream's ext-apps Playwright suite against a mcpkit-Go drop-in.

Two modes:

  1. Native (default) — fixture + basic-host + Playwright run on the host.
     Fast iteration; visual checks compare against the committed Linux
     baseline (which will fail outside Linux unless --update-snapshots is set).

  2. Docker (--docker) — fixture cross-compiled for linux/amd64 on the host,
     then everything runs inside mcr.microsoft.com/playwright:v1.57.0-noble.
     Snapshots produced are byte-identical to upstream's CI image.

Upstream's tests are written against upstream's own example servers (select
by server name in basic-host's dropdown, assert screenshots, etc.). To run
them against mcpkit, we substitute a mcpkit-Go server that exposes the same
tool name + resource URI + HTML as upstream's example. basic-host (upstream,
port 8080) remains the test harness; only the MCP server URL in SERVERS env
points at our Go fixture instead of upstream's TS server.

Each upstream example requires its own mcpkit-Go fixture under
examples/apps/compat/<name>/. Run `--all` to sweep every registered fixture
sequentially with a final pass/fail summary.

Prerequisites:
  - Native mode: Node.js 22+ with npx, bun, Go
  - Docker mode: docker, Go (host-side cross-compile only)

Usage (CLI flags or env vars; CLI wins):
  uv run scripts/apps_playwright_test.py                       # native, default example
  uv run scripts/apps_playwright_test.py --docker              # CI-identical Docker mode
  uv run scripts/apps_playwright_test.py --example pdf-server  # pick a fixture
  uv run scripts/apps_playwright_test.py --docker --all        # sweep every fixture

Environment variables (preserved from the bash predecessor for drop-in
Makefile/CI compatibility — CLI flags override):
  EXT_APPS_DIR       Path to ext-apps checkout (default: /tmp/ext-apps)
  HARNESS_PORT       basic-host HTTP port (default: 8080)
  SANDBOX_PORT       basic-host sandbox port (default: 8081)
  FIXTURE_PORT       mcpkit fixture port (default: 3101)
  EXAMPLE            Upstream example folder name (default: basic-server-vanillajs)
  VERBOSE            Set to 1 for --reporter=list
  UPDATE_SNAPSHOTS   Set to 1 to (re)generate the baseline PNG. Run under
                     DOCKER=1 for the canonical Linux baseline; the committed
                     PNG has no platform suffix and is pinned to Linux.
  DOCKER             Set to 1 for Docker mode (or pass --docker).
  HEADLESS           Set to 1 to force-disable the visible browser in native
                     mode (default is headed locally).
  HEADED             Set to 1 (default in native mode) for a visible browser.
                     Implies --workers=1 + --reporter=list. Native mode only;
                     errors out under Docker.
  DEBUG_PW           Set to 1 to launch Playwright's Inspector. Native only.
  UI                 Set to 1 to launch Playwright's UI mode. Native only.

Runs on any platform with Python 3.9+. Stdlib only.
"""

from __future__ import annotations

import argparse
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Optional

# Shared with scripts/apps_demo.py — constants, fixture registry, port helpers,
# upstream clone/install/build, fixture-binary launch. See _apps_common.py.
from _apps_common import (
    DEFAULT_EXAMPLE,
    DEFAULT_EXT_APPS_DIR,
    DEFAULT_FIXTURE_PORT,
    DEFAULT_HARNESS_PORT,
    DEFAULT_SANDBOX_PORT,
    EXT_APPS_REPO,
    FIXTURES,
    FIXTURES_BY_NAME,
    Fixture,
    MCPKIT_ROOT,
    build_go_fixture,
    build_upstream_example,
    cleanup_proc,
    die,
    ensure_ext_apps_clone,
    have_cmd,
    info,
    install_upstream_deps,
    kill_port,
    port_is_free,
    start_basic_host,
    start_go_fixture,
    tail_file,
    wait_for_fixture,
    wait_for_ports_free,
    wait_for_url,
)


# --- Constants (Playwright-specific) ---------------------------------------

DOCKER_IMAGE = "mcr.microsoft.com/playwright:v1.57.0-noble"
DOCKER_VOLUME = "mcpkit-ext-apps"

# Inner Docker script — kept as bash because it runs inside a pinned Linux
# container. Cross-platform reproducibility isn't a concern there.
DOCKER_INNER_SCRIPT = "scripts/apps-playwright-docker-inner.sh"


# --- Config resolution -----------------------------------------------------


@dataclass
class Config:
    """Resolved runtime config. CLI flags override env vars override defaults."""

    docker: bool
    example: str
    all: bool
    ext_apps_dir: Path
    harness_port: int
    sandbox_port: int
    fixture_port: int
    update_snapshots: bool
    verbose: bool
    headed: bool
    headless: bool
    debug_pw: bool
    ui_mode: bool


def env_flag(name: str) -> bool:
    """Truthy if the named env var equals "1" (the bash convention)."""
    return os.environ.get(name, "") == "1"


def env_str(name: str, default: str) -> str:
    val = os.environ.get(name, "")
    return val if val else default


def env_int(name: str, default: int) -> int:
    val = os.environ.get(name, "")
    if not val:
        return default
    try:
        return int(val)
    except ValueError:
        die(f"env {name}={val!r} is not an integer")


def build_argparser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description=__doc__.split("\n", 1)[0],
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="See module docstring for full env-var contract.",
    )
    parser.add_argument(
        "--docker",
        action=argparse.BooleanOptionalAction,
        default=None,
        help="Run inside upstream's Playwright Docker image. Default: $DOCKER==1.",
    )
    parser.add_argument(
        "--example",
        default=None,
        help=f"Upstream EXAMPLE folder name. Default: $EXAMPLE or {DEFAULT_EXAMPLE!r}.",
    )
    parser.add_argument(
        "--all",
        action="store_true",
        help="Sweep every registered fixture sequentially with a final pass/fail summary.",
    )
    parser.add_argument(
        "--ext-apps-dir",
        default=None,
        help=f"Path to ext-apps checkout. Default: $EXT_APPS_DIR or {DEFAULT_EXT_APPS_DIR}.",
    )
    parser.add_argument("--harness-port", type=int, default=None)
    parser.add_argument("--sandbox-port", type=int, default=None)
    parser.add_argument("--fixture-port", type=int, default=None)
    parser.add_argument(
        "--update-snapshots",
        action="store_true",
        default=None,
        help="Regenerate the baseline PNG. Use --docker for the canonical Linux baseline.",
    )
    parser.add_argument(
        "--verbose",
        action="store_true",
        default=None,
        help="Pass --reporter=list to Playwright.",
    )
    parser.add_argument(
        "--headed",
        action=argparse.BooleanOptionalAction,
        default=None,
        help="Visible browser (native mode only). Default: headed unless --headless or --docker.",
    )
    parser.add_argument(
        "--headless",
        action="store_true",
        default=None,
        help="Force-disable the visible browser in native mode.",
    )
    parser.add_argument(
        "--debug",
        dest="debug_pw",
        action="store_true",
        default=None,
        help="Launch Playwright Inspector. Native only.",
    )
    parser.add_argument(
        "--ui",
        dest="ui_mode",
        action="store_true",
        default=None,
        help="Launch Playwright UI mode. Native only.",
    )
    return parser


def resolve_config(args: argparse.Namespace) -> Config:
    """Merge CLI flags and env vars into a single Config. CLI wins."""

    # Docker mode resolution.
    if args.docker is not None:
        docker = args.docker
    else:
        docker = env_flag("DOCKER")

    # Headed/headless logic. Native mode defaults to headed; Docker forces
    # headless. Explicit flags override.
    headless = bool(args.headless) if args.headless is not None else env_flag("HEADLESS")
    if args.headed is not None:
        headed = args.headed
    elif "HEADED" in os.environ:
        headed = env_flag("HEADED")
    else:
        # Auto: headed locally, headless under Docker or HEADLESS=1.
        headed = not (docker or headless)

    return Config(
        docker=docker,
        example=args.example or env_str("EXAMPLE", DEFAULT_EXAMPLE),
        all=args.all,
        ext_apps_dir=Path(args.ext_apps_dir or env_str("EXT_APPS_DIR", DEFAULT_EXT_APPS_DIR)),
        harness_port=args.harness_port or env_int("HARNESS_PORT", DEFAULT_HARNESS_PORT),
        sandbox_port=args.sandbox_port or env_int("SANDBOX_PORT", DEFAULT_SANDBOX_PORT),
        fixture_port=args.fixture_port or env_int("FIXTURE_PORT", DEFAULT_FIXTURE_PORT),
        update_snapshots=bool(args.update_snapshots) if args.update_snapshots is not None else env_flag("UPDATE_SNAPSHOTS"),
        verbose=bool(args.verbose) if args.verbose is not None else env_flag("VERBOSE"),
        headed=headed,
        headless=headless,
        debug_pw=bool(args.debug_pw) if args.debug_pw is not None else env_flag("DEBUG_PW"),
        ui_mode=bool(args.ui_mode) if args.ui_mode is not None else env_flag("UI"),
    )


# --- Playwright-specific helpers -------------------------------------------


def check_prerequisites(docker: bool) -> None:
    """Per-mode binary deps. Native needs npx + bun; Docker needs docker.
    Both need go for the fixture build.
    """
    required = ["go"]
    required += ["docker"] if docker else ["npx", "bun"]
    missing = [c for c in required if not have_cmd(c)]
    if missing:
        die(f"missing commands: {', '.join(missing)}. Install before running.")


# --- Native mode -----------------------------------------------------------


def write_playwright_config(ext_apps_dir: Path, snapshot_dir_abs: Path, artifacts_dir_abs: Path) -> None:
    """Generate a stripped-down playwright config that omits webServer (we
    start basic-host and the fixture ourselves) and points snapshots at
    the per-fixture committed PNG."""
    config_path = ext_apps_dir / "playwright.config.mcpkit.ts"
    config_path.write_text(
        'import baseConfig from "./playwright.config";\n'
        "\n"
        "const { webServer, ...rest } = baseConfig as any;\n"
        "\n"
        f'const snapshotDir = process.env.MCPKIT_SNAPSHOT_DIR ?? "{snapshot_dir_abs}";\n'
        f'const artifactsDir = process.env.MCPKIT_ARTIFACTS_DIR ?? "{artifacts_dir_abs}";\n'
        "\n"
        "export default {\n"
        "    ...rest,\n"
        "    // webServer omitted — caller starts basic-host + fixture externally.\n"
        "    snapshotPathTemplate: `${snapshotDir}/{arg}{ext}`,\n"
        "    outputDir: artifactsDir,\n"
        "};\n"
    )


def run_native(config: Config, fixture: Fixture) -> int:
    """Native (host) execution path."""
    snapshot_dir = MCPKIT_ROOT / fixture.fixture_dir / "__snapshots__"
    results_dir = MCPKIT_ROOT / fixture.fixture_dir / ".test-results"
    artifacts_dir = results_dir / "artifacts"
    report_dir = results_dir / "report"
    for d in (snapshot_dir, artifacts_dir, report_dir):
        d.mkdir(parents=True, exist_ok=True)

    platform_lower = sys.platform.lower()
    is_linux = platform_lower.startswith("linux")
    if not is_linux and not config.update_snapshots:
        info("")
        info(f"NOTE: native mode on {platform_lower} will pass 'loads app UI' but")
        info("      fail 'screenshot matches golden' against the Docker-pinned")
        info("      Linux baseline. Run visual checks with --docker.")
        info("")

    ensure_ext_apps_clone(config.ext_apps_dir)
    write_playwright_config(config.ext_apps_dir, snapshot_dir, artifacts_dir)

    install_upstream_deps(config.ext_apps_dir)

    info("Installing Playwright Chromium...")
    rc = subprocess.run(
        ["npx", "playwright", "install", "--with-deps", "chromium"],
        cwd=config.ext_apps_dir,
        check=False,
    ).returncode
    if rc != 0:
        die("playwright install failed", code=rc)

    build_upstream_example(config.ext_apps_dir, fixture.example)

    fixture_bin = Path(f"/tmp/mcpkit-fixture-{Path(fixture.fixture_dir).name}")
    build_go_fixture(MCPKIT_ROOT / fixture.fixture_dir, fixture_bin)

    fixture_proc: Optional[subprocess.Popen] = None
    harness_proc: Optional[subprocess.Popen] = None
    fixture_log = Path("/tmp/mcpkit-fixture.log")
    harness_log = Path("/tmp/basic-host.log")

    def cleanup() -> None:
        cleanup_proc(fixture_proc)
        cleanup_proc(harness_proc)
        # Sweep ports — basic-host's bun process spawns children.
        for port in (config.harness_port, config.sandbox_port, config.fixture_port):
            kill_port(port)

    try:
        # Start fixture.
        kill_port(config.fixture_port)
        info(f"Starting mcpkit fixture on port {config.fixture_port}...")
        fixture_proc = start_go_fixture(
            fixture_bin,
            config.fixture_port,
            ext_apps_dir=config.ext_apps_dir,
            log_file=fixture_log,
        )
        if not wait_for_fixture(config.fixture_port, timeout_s=20):
            info(f"ERROR: fixture failed to start. Tail of {fixture_log}:")
            tail_file(fixture_log, 20)
            return 1
        info(f"Fixture ready on :{config.fixture_port}")

        # Start basic-host pointing at the fixture.
        for port in (config.harness_port, config.sandbox_port):
            kill_port(port)
        time.sleep(1)

        info(
            f"Starting basic-host on {config.harness_port} (sandbox {config.sandbox_port}), "
            "SERVERS pointing at fixture..."
        )
        harness_proc = start_basic_host(
            config.ext_apps_dir,
            f"http://localhost:{config.fixture_port}/mcp",
            harness_port=config.harness_port,
            sandbox_port=config.sandbox_port,
            log_file=harness_log,
        )
        if not wait_for_url(f"http://localhost:{config.harness_port}/", timeout_s=60):
            info(f"ERROR: basic-host failed to start within 60s. Tail of {harness_log}:")
            tail_file(harness_log, 30)
            return 1
        info(f"basic-host ready on :{config.harness_port}")

        # Compose Playwright args.
        pw_args = []
        if config.verbose:
            pw_args.append("--reporter=list")
        if config.update_snapshots:
            pw_args.append("--update-snapshots")

        if config.ui_mode:
            pw_args.append("--ui")
        elif config.debug_pw:
            pw_args += ["--debug", "--workers=1", "--reporter=list"]
        elif config.headed:
            pw_args += ["--headed", "--workers=1", "--reporter=list"]

        info("")
        info("=== Running upstream Playwright tests against mcpkit fixture (native) ===")
        info(f"Example:    {fixture.example}")
        info(f"Fixture:    http://localhost:{config.fixture_port}/mcp")
        info(f"Harness:    http://localhost:{config.harness_port}")
        info(f"Snapshots:  {snapshot_dir}")
        if config.update_snapshots:
            info("MODE:       --update-snapshots (regenerating baseline)")
        if config.ui_mode:
            info("MODE:       --ui (Playwright UI runner)")
        elif config.debug_pw:
            info("MODE:       --debug (Playwright Inspector, step-through)")
        elif config.headed:
            info("MODE:       --headed (visible browser, serial)")
        info("")

        pw_env = os.environ.copy()
        pw_env["EXAMPLE"] = fixture.example
        pw_env["PLAYWRIGHT_HTML_OUTPUT_DIR"] = str(report_dir)
        pw_env["PLAYWRIGHT_HTML_OPEN"] = "never"
        rc = subprocess.run(
            [
                "npx", "playwright", "test",
                "--config=playwright.config.mcpkit.ts",
                "--grep", fixture.grep_pattern,
                *pw_args,
            ],
            cwd=config.ext_apps_dir,
            env=pw_env,
            check=False,
        ).returncode

        report_outcome(rc, fixture, docker=False, artifacts_dir=artifacts_dir, report_dir=report_dir)
        return rc
    finally:
        cleanup()


# --- Docker mode -----------------------------------------------------------


def run_docker(config: Config, fixture: Fixture) -> int:
    snapshot_dir_abs = MCPKIT_ROOT / fixture.fixture_dir / "__snapshots__"
    results_dir = MCPKIT_ROOT / fixture.fixture_dir / ".test-results"
    artifacts_dir_abs = results_dir / "artifacts"
    report_dir_abs = results_dir / "report"
    for d in (snapshot_dir_abs, artifacts_dir_abs, report_dir_abs):
        d.mkdir(parents=True, exist_ok=True)

    snapshot_dir_container = f"/mcpkit/{fixture.fixture_dir}/__snapshots__"
    artifacts_dir_container = f"/mcpkit/{fixture.fixture_dir}/.test-results/artifacts"
    report_dir_container = f"/mcpkit/{fixture.fixture_dir}/.test-results/report"

    fixture_bin_host = MCPKIT_ROOT / f".tmp-fixture-linux-amd64-{Path(fixture.fixture_dir).name}"
    fixture_bin_container = "/tmp/fixture-linux-amd64"

    try:
        build_go_fixture(MCPKIT_ROOT / fixture.fixture_dir, fixture_bin_host, linux_amd64=True)

        info(f"Pulling {DOCKER_IMAGE} if needed...")
        subprocess.run(["docker", "pull", "--quiet", DOCKER_IMAGE], check=False)

        info("")
        info(f"=== Launching {DOCKER_IMAGE} (volume: {DOCKER_VOLUME}) ===")
        env_pass = {
            "EXAMPLE": fixture.example,
            "GREP_PATTERN": fixture.grep_pattern,
            "FIXTURE_BIN": fixture_bin_container,
            "MCPKIT_SNAPSHOT_DIR": snapshot_dir_container,
            "MCPKIT_ARTIFACTS_DIR": artifacts_dir_container,
            "MCPKIT_REPORT_DIR": report_dir_container,
            "HARNESS_PORT": str(config.harness_port),
            "SANDBOX_PORT": str(config.sandbox_port),
            "FIXTURE_PORT": str(config.fixture_port),
            "EXT_APPS_DIR": "/ext-apps",
            "EXT_APPS_REPO": EXT_APPS_REPO,
            "UPDATE_SNAPSHOTS": "1" if config.update_snapshots else "",
            "VERBOSE": "1" if config.verbose else "",
        }
        docker_cmd = ["docker", "run", "--rm"]
        for k, v in env_pass.items():
            docker_cmd += ["-e", f"{k}={v}"]
        docker_cmd += [
            "-v", f"{MCPKIT_ROOT}:/mcpkit",
            "-v", f"{DOCKER_VOLUME}:/ext-apps",
            "-v", f"{fixture_bin_host}:{fixture_bin_container}:ro",
            DOCKER_IMAGE,
            "bash", f"/mcpkit/{DOCKER_INNER_SCRIPT}",
        ]
        rc = subprocess.run(docker_cmd, check=False).returncode

        report_outcome(rc, fixture, docker=True, artifacts_dir=artifacts_dir_abs, report_dir=report_dir_abs)
        return rc
    finally:
        if fixture_bin_host.exists():
            try:
                fixture_bin_host.unlink()
            except OSError:
                pass


# --- Outcome reporting -----------------------------------------------------


def report_outcome(rc: int, fixture: Fixture, *, docker: bool, artifacts_dir: Path, report_dir: Path) -> None:
    info("")
    suffix = ", docker" if docker else ""
    if rc == 0:
        info(f"=== PASSED ({fixture.example} against mcpkit fixture{suffix}) ===")
    else:
        info(f"=== FAILED ({fixture.example} against mcpkit fixture{suffix}, exit {rc}) ===")
        info("")
        info("Artifacts (actual / diff PNGs, traces) under:")
        info(f"  {artifacts_dir}")
        info("HTML report:")
        info(f"  {report_dir}/index.html")


# --- All-fixture sweep -----------------------------------------------------


def run_all(config: Config) -> int:
    failed: list[tuple[str, int]] = []
    shared_ports = [config.harness_port, config.sandbox_port, config.fixture_port]
    for i, fixture in enumerate(FIXTURES, 1):
        info("")
        info(f"=== [{i}/{len(FIXTURES)}] Running {fixture.example} ===")
        if config.docker:
            rc = run_docker(config, fixture)
        else:
            rc = run_native(config, fixture)
        if rc == 0:
            info(f"PASS: {fixture.example}")
        else:
            info(f"FAIL: {fixture.example} (exit {rc})")
            failed.append((fixture.example, rc))
        # Issue 601: between-fixture port-cleanup wait. The per-fixture
        # finally block already killed the processes; this lets the kernel
        # release sockets out of TIME_WAIT before the next fixture binds,
        # which was the integration-server flake we saw under --all.
        if i < len(FIXTURES) and not wait_for_ports_free(shared_ports, timeout_s=10):
            info(f"WARN: ports {shared_ports} still bound after 10s; next fixture may race")

    info("")
    info("=== Summary ===")
    info(f"Total:  {len(FIXTURES)}")
    info(f"Passed: {len(FIXTURES) - len(failed)}")
    info(f"Failed: {len(failed)}")
    for name, rc in failed:
        info(f"  - {name} (exit {rc})")
    return 0 if not failed else 1


# --- Entry point ------------------------------------------------------------


def main() -> int:
    parser = build_argparser()
    args = parser.parse_args()
    config = resolve_config(args)

    # Guard rail: visible-browser modes don't make sense under Docker.
    # Auto-default already silenced HEADED under Docker; fail only when the
    # user explicitly opted into a visible-browser flag.
    if config.docker:
        for name, val in [("--debug / DEBUG_PW", config.debug_pw), ("--ui / UI", config.ui_mode)]:
            if val:
                die(
                    f"{name} is not supported with --docker — visible-browser modes "
                    "need a display, and X11 forwarding into the Playwright "
                    "container isn't worth the setup cost. Re-run without --docker."
                )

    check_prerequisites(config.docker)

    if config.all:
        return run_all(config)

    fixture = FIXTURES_BY_NAME.get(config.example)
    if fixture is None:
        info(f"ERROR: no mcpkit fixture for upstream example '{config.example}'")
        info("Available fixtures:")
        for f in FIXTURES:
            info(f"  {f.example:<32}  →  {f.fixture_dir}")
        return 1

    if config.docker:
        return run_docker(config, fixture)
    return run_native(config, fixture)


if __name__ == "__main__":
    sys.exit(main())
