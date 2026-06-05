"""Shared utilities for the apps/compat harness scripts.

Both scripts/apps_playwright_test.py and scripts/apps_demo.py speak the
same language at the lower layers: same FIXTURES registry, same ports,
same upstream-clone, same fixture-binary launch + MCP-initialize probe.
This module is the seam where that overlap lives in one place so the
two scripts stay consistent.

What's here:
  - Constants: ports, repo, image, MCPKIT_ROOT, EXT_APPS_REPO
  - Fixture dataclass + FIXTURES list (EXAMPLE -> mcpkit drop-in)
  - Port helpers: have_cmd, kill_port, port_is_free, wait_for_ports_free
  - URL probing: wait_for_url, wait_for_fixture (MCP initialize POST)
  - Upstream: ensure_ext_apps_clone, install_upstream_deps, build_upstream_example
  - Launchers: start_go_fixture, start_basic_host
  - Cleanup: cleanup_proc, tail_file

What's NOT here:
  - Playwright-specific config (snapshot dirs, --update-snapshots flags,
    headed/headless, Docker cross-compile + docker run) -- stays in
    apps_playwright_test.py.
  - Demo-specific launchers (MCPJam, upstream TS server start) -- live
    in apps_demo.py.

Stdlib only.
"""

from __future__ import annotations

import os
import shutil
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Optional


# --- Constants --------------------------------------------------------------

EXT_APPS_REPO = "https://github.com/modelcontextprotocol/ext-apps.git"

DEFAULT_EXT_APPS_DIR = "/tmp/ext-apps"
DEFAULT_HARNESS_PORT = 8080
DEFAULT_SANDBOX_PORT = 8081
DEFAULT_FIXTURE_PORT = 3101
DEFAULT_EXAMPLE = "basic-vanillajs"

# Repo root (one level up from scripts/).
MCPKIT_ROOT = Path(__file__).resolve().parent.parent


# --- Fixture registry ------------------------------------------------------


@dataclass(frozen=True)
class Fixture:
    """One row of the mcpkit-fixture / upstream-example mapping.

    `name` is the user-facing identifier — the directory basename under
    examples/apps/compat/<name>/. This is what `EXAMPLE=<name>` should
    be on the command line, and what the per-fixture README's "## Run
    it" block uses. Stays short and matches what `ls` shows.

    `upstream_example` is the matching upstream `ext-apps/examples/<dir>/`
    name. Sometimes equal to `name` (quickstart, pdf-server, debug-server),
    sometimes prefixed with `basic-server-` or suffixed with `-server`
    depending on upstream's naming. Used internally to find upstream's
    iframe HTML, start the upstream TS reference server, and build the
    example.

    `grep_pattern` is the regex Playwright uses to scope to the matching
    describe block in upstream's servers.spec.ts. Demo mode ignores it.
    """

    name: str
    fixture_dir: str
    upstream_example: str
    grep_pattern: str


# Ordering matches the examples/apps/compat README's reading-order table.
FIXTURES: list[Fixture] = [
    Fixture("basic-vanillajs", "examples/apps/compat/basic-vanillajs", "basic-server-vanillajs", "Vanilla JS"),
    Fixture("basic-preact", "examples/apps/compat/basic-preact", "basic-server-preact", r"\(Preact\)"),
    Fixture("basic-react", "examples/apps/compat/basic-react", "basic-server-react", r"\(React\)"),
    Fixture("basic-solid", "examples/apps/compat/basic-solid", "basic-server-solid", r"\(Solid\)"),
    Fixture("basic-svelte", "examples/apps/compat/basic-svelte", "basic-server-svelte", r"\(Svelte\)"),
    Fixture("basic-vue", "examples/apps/compat/basic-vue", "basic-server-vue", r"\(Vue\)"),
    Fixture("quickstart", "examples/apps/compat/quickstart", "quickstart", "Quickstart MCP App Server"),
    Fixture("transcript", "examples/apps/compat/transcript", "transcript-server", "Transcript Server"),
    Fixture("sheet-music", "examples/apps/compat/sheet-music", "sheet-music-server", "Sheet Music Server"),
    # "Integration Test Server" substring-matches BOTH the standard describe
    # ("Integration Test Server") and the interactions describe
    # ("Integration Test Server - Interactions") in upstream's spec.
    Fixture("integration", "examples/apps/compat/integration", "integration-server", "Integration Test Server"),
    Fixture("map", "examples/apps/compat/map", "map-server", "CesiumJS Map Server"),
    Fixture("threejs", "examples/apps/compat/threejs", "threejs-server", "Three.js Server"),
    Fixture("shadertoy", "examples/apps/compat/shadertoy", "shadertoy-server", "ShaderToy Server"),
    Fixture("wiki-explorer", "examples/apps/compat/wiki-explorer", "wiki-explorer-server", "Wiki Explorer"),
    Fixture("budget-allocator", "examples/apps/compat/budget-allocator", "budget-allocator-server", "Budget Allocator Server"),
    Fixture("scenario-modeler", "examples/apps/compat/scenario-modeler", "scenario-modeler-server", "SaaS Scenario Modeler"),
    Fixture("system-monitor", "examples/apps/compat/system-monitor", "system-monitor-server", "System Monitor Server"),
    Fixture("cohort-heatmap", "examples/apps/compat/cohort-heatmap", "cohort-heatmap-server", "Cohort Heatmap Server"),
    Fixture("customer-segmentation", "examples/apps/compat/customer-segmentation", "customer-segmentation-server", "Customer Segmentation Server"),
    Fixture("debug-server", "examples/apps/compat/debug-server", "debug-server", "Debug MCP App Server"),
    # Match all PDF-related describes: standard ("PDF Server"), pdf-annotations
    # / pdf-incremental-load ("PDF Server - ..."), and pdf-viewer-zoom
    # ("PDF Viewer - ..."). pdf-annotations-api ("PDF Annotation - API ...") is
    # LLM-gated upstream (ANTHROPIC_API_KEY) and auto-skips when no key is set.
    Fixture("pdf-server", "examples/apps/compat/pdf-server", "pdf-server", r"PDF (Server|Viewer|Annotation)"),
]

FIXTURES_BY_NAME: dict[str, Fixture] = {f.name: f for f in FIXTURES}


# --- Output helpers --------------------------------------------------------


def info(msg: str) -> None:
    """Print one line, flush immediately (so log streaming stays live)."""
    print(msg, flush=True)


def die(msg: str, code: int = 1) -> None:
    """Print ERROR: <msg> to stderr and exit."""
    print(f"ERROR: {msg}", file=sys.stderr)
    sys.exit(code)


def tail_file(path: Path, n: int) -> None:
    """Best-effort: print the last n lines of a log file."""
    try:
        lines = path.read_text(errors="replace").splitlines()
    except OSError:
        return
    for line in lines[-n:]:
        info(line)


# --- Command discovery -----------------------------------------------------


def have_cmd(cmd: str) -> bool:
    return shutil.which(cmd) is not None


# --- Port helpers ----------------------------------------------------------


def kill_port(port: int) -> None:
    """Best-effort: SIGKILL anything listening on the given port via lsof.

    No-op on systems without lsof installed.
    """
    if not have_cmd("lsof"):
        return
    try:
        result = subprocess.run(
            ["lsof", "-ti", f":{port}"],
            capture_output=True,
            text=True,
            check=False,
        )
        pids = result.stdout.strip().splitlines()
        for pid in pids:
            try:
                os.kill(int(pid), signal.SIGKILL)
            except (ValueError, ProcessLookupError, PermissionError):
                pass
    except Exception:
        pass


def port_is_free(port: int) -> bool:
    """True if nothing is currently bound on localhost:port.

    Uses a plain bind() probe with SO_REUSEADDR=0 so a socket still in
    TIME_WAIT counts as in-use -- that's the exact race --all hits between
    fixtures, and what the next process's Popen will see.
    """
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        sock.bind(("127.0.0.1", port))
        return True
    except OSError:
        return False
    finally:
        sock.close()


def wait_for_ports_free(ports: list[int], timeout_s: int = 10) -> bool:
    """Poll until none of the listed ports are bound (or timeout)."""
    deadline = time.monotonic() + timeout_s
    while time.monotonic() < deadline:
        if all(port_is_free(p) for p in ports):
            return True
        time.sleep(0.5)
    return False


def cleanup_proc(proc: Optional[subprocess.Popen], timeout_s: int = 5) -> None:
    """Terminate then kill a process; swallow errors."""
    if proc is None or proc.poll() is not None:
        return
    try:
        proc.terminate()
        try:
            proc.wait(timeout=timeout_s)
        except subprocess.TimeoutExpired:
            proc.kill()
    except Exception:
        pass


# --- URL / MCP probes ------------------------------------------------------


def wait_for_url(
    url: str,
    timeout_s: int,
    *,
    method: str = "GET",
    data: Optional[bytes] = None,
    headers: Optional[dict] = None,
) -> bool:
    """Poll the URL until it responds (any 2xx-5xx) or timeout."""
    deadline = time.monotonic() + timeout_s
    req = urllib.request.Request(url, data=data, method=method, headers=headers or {})
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(req, timeout=2) as resp:
                _ = resp.read(1)
                return True
        except urllib.error.HTTPError:
            # 4xx/5xx still means the server is alive.
            return True
        except (urllib.error.URLError, ConnectionError, TimeoutError):
            time.sleep(1)
    return False


def wait_for_fixture(port: int, timeout_s: int = 20) -> bool:
    """Send a real MCP initialize to confirm the fixture is fully booted.

    Used after starting a Go fixture or an upstream TS server -- a plain
    TCP-bind check doesn't tell us the MCP dispatcher is wired up yet.
    """
    body = (
        b'{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":'
        b'"2024-11-05","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}'
    )
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json, text/event-stream",
    }
    return wait_for_url(
        f"http://localhost:{port}/mcp",
        timeout_s,
        method="POST",
        data=body,
        headers=headers,
    )


# --- Upstream clone + build ------------------------------------------------


def ensure_ext_apps_clone(ext_apps_dir: Path) -> None:
    """Clone ext-apps if missing, or pull if present. Best-effort pull."""
    if (ext_apps_dir / ".git").exists():
        info(f"Updating ext-apps in {ext_apps_dir}...")
        subprocess.run(["git", "pull", "--quiet"], cwd=ext_apps_dir, check=False)
    else:
        info(f"Cloning ext-apps to {ext_apps_dir}...")
        subprocess.run(
            ["git", "clone", "--quiet", EXT_APPS_REPO, str(ext_apps_dir)],
            check=True,
        )


def install_upstream_deps(ext_apps_dir: Path, *, only_if_missing: bool = True) -> None:
    """Run `npm install` in the ext-apps root.

    If only_if_missing is True, skips when node_modules/@playwright already
    exists (cold-install indicator). Set to False to force a reinstall.
    """
    if only_if_missing and (ext_apps_dir / "node_modules" / "@playwright").exists():
        return
    info("Installing upstream npm deps...")
    subprocess.run(
        ["npm", "install", "--silent", "--no-audit", "--no-fund"],
        cwd=ext_apps_dir,
        check=False,
    )


def build_upstream_example(ext_apps_dir: Path, example: str) -> None:
    """Run `npm run build` in ext_apps_dir/examples/<example>/.

    Produces dist/mcp-app.html and (for examples that build a server)
    dist/index.js. Required before launching basic-host (needs the html)
    or starting upstream TS (needs the index.js for the dist/ path).
    """
    info(f"Building {example} upstream UI...")
    subprocess.run(
        ["npm", "run", "build"],
        cwd=ext_apps_dir / "examples" / example,
        check=False,
    )


# --- Go fixture build + launch ---------------------------------------------


def build_go_fixture(fixture_dir: Path, output_bin: Path, *, linux_amd64: bool = False) -> None:
    """`go build -o <output_bin> .` from inside fixture_dir.

    linux_amd64=True cross-compiles for the Playwright Docker image; the
    demo target leaves it False for a native binary the host can run.
    """
    env = os.environ.copy()
    if linux_amd64:
        env["GOOS"] = "linux"
        env["GOARCH"] = "amd64"
    info(f"Building mcpkit fixture: {fixture_dir.relative_to(MCPKIT_ROOT)}...")
    subprocess.run(
        ["go", "build", "-o", str(output_bin), "."],
        cwd=fixture_dir,
        env=env,
        check=True,
    )


def start_go_fixture(
    bin_path: Path,
    port: int,
    *,
    ext_apps_dir: Path,
    log_file: Path,
) -> subprocess.Popen:
    """Popen the mcpkit Go fixture binary on the given port.

    The fixture reads $PORT and $EXT_APPS_DIR (the latter for the iframe
    HTML path). Caller is responsible for `wait_for_fixture()` + cleanup.
    """
    fixture_env = os.environ.copy()
    fixture_env["EXT_APPS_DIR"] = str(ext_apps_dir)
    fixture_env["PORT"] = str(port)
    return subprocess.Popen(
        [str(bin_path)],
        stdout=log_file.open("w"),
        stderr=subprocess.STDOUT,
        env=fixture_env,
    )


# --- basic-host launch -----------------------------------------------------


def start_basic_host(
    ext_apps_dir: Path,
    server_url: str,
    *,
    harness_port: int,
    sandbox_port: int,
    log_file: Path,
) -> subprocess.Popen:
    """Start upstream's basic-host pointing at the given MCP server URL.

    SERVERS is a JSON array because basic-host accepts a list (multi-server
    runs put 25+ entries here; compat runs use exactly one). Caller is
    responsible for `wait_for_url` on the harness root + cleanup.
    """
    servers_json = f'["{server_url}"]'
    harness_env = os.environ.copy()
    harness_env["SERVERS"] = servers_json
    harness_env["HOST_PORT"] = str(harness_port)
    harness_env["SANDBOX_PORT"] = str(sandbox_port)
    return subprocess.Popen(
        ["npm", "run", "start"],
        cwd=ext_apps_dir / "examples" / "basic-host",
        stdout=log_file.open("w"),
        stderr=subprocess.STDOUT,
        env=harness_env,
    )
