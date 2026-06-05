#!/usr/bin/env python3
"""Interactive demo runner for ext-apps examples.

Two axes:

  --server=go|upstream    pick the MCP server on :3101
    go (default)          mcpkit-Go fixture built from examples/apps/compat/
    upstream              upstream TS reference server from $EXT_APPS_DIR

  --renderer=mcpjam|basic-host    pick the renderer
    mcpjam (default)      launch `npx @mcpjam/inspector@latest`
                          opens its own browser; you paste the MCP URL
                          into the server list. Wire-level inspection.
    basic-host            launch upstream's basic-host harness
                          renders the App's iframe + bridge JS. Visual
                          demo. Needs the example's dist/mcp-app.html.

Replaces the older `make demo-app` (server=upstream, renderer=basic-host) and
`make inspect-app` (server=upstream, renderer=mcpjam). The split is now on
the *server* axis, not the renderer:

  make demo-app EXAMPLE=<name>        # default: server=go,       renderer=mcpjam
  make demo-upstream EXAMPLE=<name>   # default: server=upstream, renderer=mcpjam

Either can be overridden with RENDERER=basic-host.

Usage:
  uv run scripts/apps_demo.py --example basic-server-vanillajs
  uv run scripts/apps_demo.py --example basic-server-vanillajs --renderer basic-host
  uv run scripts/apps_demo.py --example lazy-auth-server --server upstream

Env vars (preserved from the bash predecessors for drop-in Makefile compatibility):
  EXT_APPS_DIR       Path to ext-apps checkout (default: /tmp/ext-apps)
  HARNESS_PORT       basic-host HTTP port (default: 8080)
  SANDBOX_PORT       basic-host sandbox port (default: 8081)
  SERVER_PORT        MCP server port on which Go or upstream binds (default: 3101)
  EXAMPLE            upstream example folder name (required)
  SERVER             go | upstream (default: go)
  RENDERER           mcpjam | basic-host (default: mcpjam)
  OPEN               1 to auto-open basic-host in a browser (basic-host
                     renderer only; MCPJam manages its own browser).

Foreground only — Ctrl-C tears the upstream/Go server down. MCPJam
manages its own lifecycle; when you quit MCPJam you'll need to Ctrl-C
this script to release the upstream/Go server too.

Stdlib only.
"""

from __future__ import annotations

import argparse
import os
import subprocess
import sys
import time
from pathlib import Path
from typing import Optional

from _apps_common import (
    DEFAULT_EXT_APPS_DIR,
    DEFAULT_FIXTURE_PORT,
    DEFAULT_HARNESS_PORT,
    DEFAULT_SANDBOX_PORT,
    FIXTURES,
    FIXTURES_BY_NAME,
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
    start_basic_host,
    start_go_fixture,
    tail_file,
    wait_for_fixture,
    wait_for_url,
)


# --- Config ----------------------------------------------------------------


VALID_SERVERS = ("go", "upstream")
VALID_RENDERERS = ("mcpjam", "basic-host")


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


def env_flag(name: str) -> bool:
    return os.environ.get(name, "") == "1"


def build_argparser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description=__doc__.split("\n", 1)[0],
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="See module docstring for full env-var contract.",
    )
    parser.add_argument(
        "--example",
        default=None,
        help="Upstream example folder name (default: $EXAMPLE). Required.",
    )
    parser.add_argument(
        "--server",
        choices=VALID_SERVERS,
        default=None,
        help="MCP server on :3101. Default: $SERVER or 'go'.",
    )
    parser.add_argument(
        "--renderer",
        choices=VALID_RENDERERS,
        default=None,
        help="Renderer. Default: $RENDERER or 'mcpjam'.",
    )
    parser.add_argument("--ext-apps-dir", default=None)
    parser.add_argument("--harness-port", type=int, default=None)
    parser.add_argument("--sandbox-port", type=int, default=None)
    parser.add_argument("--server-port", type=int, default=None,
                        help="MCP server port (Go fixture or upstream TS). Default 3101.")
    parser.add_argument("--open", dest="open_browser", action=argparse.BooleanOptionalAction,
                        default=None, help="Auto-open basic-host in browser. Basic-host only. Default on.")
    return parser


# --- Server launchers (server-axis) ---------------------------------------


def start_upstream_ts_server(
    ext_apps_dir: Path,
    example: str,
    port: int,
    log_file: Path,
) -> subprocess.Popen:
    """Start upstream's TS server for one example on the given port.

    Some examples ship `dist/index.js` (built via `bun build main.ts`);
    others (quickstart, lazy-auth) only build the iframe and expect
    `tsx main.ts` to run the server directly. We probe the dist path
    first and fall back to tsx.
    """
    example_dir = ext_apps_dir / "examples" / example
    if (example_dir / "dist" / "index.js").exists():
        cmd = ["node", "dist/index.js"]
    elif (example_dir / "main.ts").exists():
        cmd = ["npx", "tsx", "main.ts"]
    else:
        die(f"don't know how to start {example} — no dist/index.js or main.ts in {example_dir}")
    server_env = os.environ.copy()
    server_env["PORT"] = str(port)
    return subprocess.Popen(
        cmd,
        cwd=example_dir,
        stdout=log_file.open("w"),
        stderr=subprocess.STDOUT,
        env=server_env,
    )


def ensure_go_fixture_exists(example: str) -> Path:
    """Resolve a Go fixture path for the given upstream example, or die
    with a friendly redirect to demo-upstream if none exists.
    """
    fixture = FIXTURES_BY_NAME.get(example)
    if fixture is None:
        info("")
        info(f"ERROR: no mcpkit-Go drop-in for upstream example '{example}'.")
        info("")
        info(f"  Try `make demo-upstream EXAMPLE={example}` to browse the upstream TS reference instead.")
        info("")
        info("Available Go fixtures:")
        for f in FIXTURES:
            info(f"  {f.example}")
        sys.exit(1)
    return MCPKIT_ROOT / fixture.fixture_dir


# --- Renderer launchers (renderer-axis) -----------------------------------


def start_mcpjam(server_url: str) -> subprocess.Popen:
    """Run `npx @mcpjam/inspector@latest` in the foreground.

    MCPJam opens its own browser tab; we don't manage the URL. We print
    a banner telling the user to paste `server_url` into MCPJam's server
    list once it's loaded.
    """
    return subprocess.Popen(
        ["npx", "-y", "@mcpjam/inspector@latest"],
        stdout=sys.stdout,
        stderr=sys.stderr,
        env=os.environ.copy(),
    )


def open_in_browser(url: str) -> None:
    """Best-effort: open url in the system default browser."""
    if have_cmd("open"):
        subprocess.run(["open", url], check=False)
    elif have_cmd("xdg-open"):
        subprocess.run(["xdg-open", url], check=False)


# --- Main flow -------------------------------------------------------------


def main() -> int:
    parser = build_argparser()
    args = parser.parse_args()

    example = args.example or env_str("EXAMPLE", "")
    if not example:
        die("missing --example (or env EXAMPLE).")

    server = args.server or env_str("SERVER", "go")
    if server not in VALID_SERVERS:
        die(f"--server must be one of {VALID_SERVERS}, got {server!r}")

    renderer = args.renderer or env_str("RENDERER", "mcpjam")
    if renderer not in VALID_RENDERERS:
        die(f"--renderer must be one of {VALID_RENDERERS}, got {renderer!r}")

    ext_apps_dir = Path(args.ext_apps_dir or env_str("EXT_APPS_DIR", DEFAULT_EXT_APPS_DIR))
    harness_port = args.harness_port or env_int("HARNESS_PORT", DEFAULT_HARNESS_PORT)
    sandbox_port = args.sandbox_port or env_int("SANDBOX_PORT", DEFAULT_SANDBOX_PORT)
    server_port = args.server_port or env_int("SERVER_PORT", DEFAULT_FIXTURE_PORT)

    if args.open_browser is None:
        open_browser = env_str("OPEN", "1") == "1"
    else:
        open_browser = args.open_browser

    # Prereqs vary by server + renderer:
    needed = []
    if server == "go":
        needed.append("go")
    if server == "upstream" or renderer == "basic-host":
        needed += ["npx", "bun"]
    if renderer == "mcpjam":
        needed.append("npx")
    missing = [c for c in dict.fromkeys(needed) if not have_cmd(c)]
    if missing:
        die(f"missing commands: {', '.join(missing)}. Install before running.")

    server_url = f"http://localhost:{server_port}/mcp"

    server_proc: Optional[subprocess.Popen] = None
    harness_proc: Optional[subprocess.Popen] = None
    renderer_proc: Optional[subprocess.Popen] = None
    server_log = Path("/tmp/apps-demo-server.log")
    harness_log = Path("/tmp/apps-demo-harness.log")

    def cleanup() -> None:
        info("")
        info("Shutting down...")
        cleanup_proc(renderer_proc)
        cleanup_proc(harness_proc)
        cleanup_proc(server_proc)
        for port in (harness_port, sandbox_port, server_port):
            kill_port(port)

    try:
        # --- Upstream clone (needed for upstream server OR basic-host renderer
        #     OR any iframe HTML the Go fixture reads from upstream's build).
        ensure_ext_apps_clone(ext_apps_dir)

        # --- Build + start the MCP server -----------------------------------
        kill_port(server_port)
        if server == "go":
            fixture_dir = ensure_go_fixture_exists(example)
            # Go fixtures read upstream's pre-built iframe HTML at startup.
            # If upstream isn't built yet, we need to build it.
            install_upstream_deps(ext_apps_dir)
            build_upstream_example(ext_apps_dir, example)
            fixture_bin = Path(f"/tmp/mcpkit-fixture-{fixture_dir.name}")
            build_go_fixture(fixture_dir, fixture_bin)
            info(f"Starting mcpkit Go fixture on :{server_port}...")
            server_proc = start_go_fixture(
                fixture_bin,
                server_port,
                ext_apps_dir=ext_apps_dir,
                log_file=server_log,
            )
        else:
            # Upstream TS server.
            install_upstream_deps(ext_apps_dir)
            build_upstream_example(ext_apps_dir, example)
            info(f"Starting upstream TS server for {example} on :{server_port}...")
            server_proc = start_upstream_ts_server(ext_apps_dir, example, server_port, server_log)

        if not wait_for_fixture(server_port, timeout_s=30):
            info(f"ERROR: MCP server failed to start on :{server_port}. Tail of {server_log}:")
            tail_file(server_log, 30)
            return 1
        info(f"MCP server ready on :{server_port}")

        # --- Start the renderer --------------------------------------------
        if renderer == "basic-host":
            for port in (harness_port, sandbox_port):
                kill_port(port)
            time.sleep(1)
            info(f"Starting basic-host on :{harness_port} (sandbox :{sandbox_port})...")
            harness_proc = start_basic_host(
                ext_apps_dir,
                server_url,
                harness_port=harness_port,
                sandbox_port=sandbox_port,
                log_file=harness_log,
            )
            harness_url = f"http://localhost:{harness_port}"
            if not wait_for_url(f"{harness_url}/", timeout_s=60):
                info(f"ERROR: basic-host failed to start. Tail of {harness_log}:")
                tail_file(harness_log, 30)
                return 1
            info(f"basic-host ready on :{harness_port}")
            _print_banner_basic_host(example, server, harness_url, server_url, server_log, harness_log)
            if open_browser:
                open_in_browser(harness_url)
            # Wait until basic-host exits (typically: user Ctrl-Cs).
            assert harness_proc is not None
            harness_proc.wait()
            return 0
        else:
            # MCPJam: foreground, opens its own browser.
            _print_banner_mcpjam(example, server, server_url, server_log)
            renderer_proc = start_mcpjam(server_url)
            renderer_proc.wait()
            return 0
    finally:
        cleanup()


# --- Banners ---------------------------------------------------------------


def _print_banner_basic_host(example: str, server: str, harness_url: str, server_url: str,
                              server_log: Path, harness_log: Path) -> None:
    info("")
    info("====================================================================")
    info(f" {example} is now serving (rendered via basic-host).")
    info(f" Server:  {server} ({'mcpkit-Go fixture' if server == 'go' else 'upstream TS'}) on {server_url}")
    info(f" Open in your browser:  {harness_url}")
    info("")
    info(" What you're seeing:")
    info("   basic-host (upstream's MCP Apps host) loads the example's iframe")
    info("   (the App) in a sandboxed iframe. The App's mcp-app-bridge.js uses")
    info("   postMessage to call back into the host -- that's why buttons inside")
    info("   the App actually do something.")
    info("")
    info(" Want the protocol surface instead of the rendered App?")
    info(f"   `make demo-app EXAMPLE={example}` (or `RENDERER=mcpjam ...`) launches MCPJam.")
    info("")
    info(" Logs:")
    info(f"   server:     {server_log}")
    info(f"   basic-host: {harness_log}")
    info("")
    info(" Press Ctrl-C to stop.")
    info("====================================================================")
    info("")


def _print_banner_mcpjam(example: str, server: str, server_url: str, server_log: Path) -> None:
    info("")
    info("====================================================================")
    info(f" {example} is now serving (MCPJam Inspector will open in your browser).")
    info(f" Server:  {server} ({'mcpkit-Go fixture' if server == 'go' else 'upstream TS'}) on {server_url}")
    info("")
    info(" When MCPJam opens, add this server URL to MCPJam's server list:")
    info(f"   {server_url}")
    info("")
    info(" Then browse tools/list, _meta.ui, the resource list, and tool-call")
    info(" payloads on the wire. To see the App *rendered* instead, re-run with")
    info(f"   RENDERER=basic-host make demo-app EXAMPLE={example}")
    info("")
    info(f" Server log:  {server_log}")
    info("")
    info(" Press Ctrl-C to stop the upstream server. MCPJam handles its own.")
    info("====================================================================")
    info("")


if __name__ == "__main__":
    sys.exit(main())
