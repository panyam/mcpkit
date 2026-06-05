#!/usr/bin/env python3
"""Bootstrap walkthrough scaffolding for compat fixtures.

For each `examples/apps/compat/<name>/` directory, writes:
  - Makefile (one-liner that includes the shared fragment)
  - walkthrough.go (stub that connects + lists tools; ready for the author to refine)
  - .gitignore (just ignores the compiled binary)
  - Converts main.go to a dual-mode dispatcher:
    `--demo` -> runDemo() (in walkthrough.go), default -> serve() (the renamed original main body).

Idempotent: skips fixtures that already have walkthrough.go, on the assumption
the author has already authored a real walkthrough there.

Used once to roll the scaffolding out across all compat fixtures. Future
fixtures can either run this again or hand-write the same shape.

Stdlib only.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

MCPKIT_ROOT = Path(__file__).resolve().parent.parent
COMPAT_ROOT = MCPKIT_ROOT / "examples" / "apps" / "compat"

# Skip basic-vanillajs (the pilot — already has a real walkthrough).
SKIP_FIXTURES = {"basic-vanillajs"}


MAKEFILE_BODY = """# Per-fixture conventions live in the shared fragment. Override
# FIXTURE_NAME / SERVER_PORT above the include if you need to.
include ../../../common/walkthrough.mk
"""


GITIGNORE_BODY_TEMPLATE = """# walkthrough.trace.json is the SOURCE of truth -- commit it.
# bundle/ is the GENERATED playable HTML + sibling JS/CSS -- commit it too,
# so docs-site can publish without regenerating. Run `make bundle` to
# refresh after editing walkthrough.go or re-recording.
#
# Only the compiled fixture binary is gitignored here.
{binary_name}
"""


WALKTHROUGH_STUB_TEMPLATE = '''package main

import (
\t"encoding/json"
\t"fmt"
\t"os"

\t"github.com/panyam/demokit"
\t"github.com/panyam/mcpkit/client"
\t"github.com/panyam/mcpkit/core"
\t"github.com/panyam/mcpkit/examples/common"
)

// runDemo is a STUB walkthrough — it connects to the fixture and lists
// its tools. Refine it: add a tools/call step per tool, attach
// common.WireRecipe(step, curl, go) for the wire reproduction, drop
// fixture-specific narrative in .Note(...). See
// examples/apps/compat/basic-vanillajs/walkthrough.go for the full
// pattern.
//
// TODO: replace this stub with a curated walkthrough.
func runDemo() {{
\tserverURL := serverURLFor3101()

\tdemo := demokit.New("{title}").
\t\tDescription("TODO: describe what this walkthrough demonstrates.").
\t\tActors(
\t\t\tdemokit.Actor("Host", "MCP Host (this client)"),
\t\t\tdemokit.Actor("Server", "mcpkit-Go fixture (make serve)"),
\t\t)

\tvar c *client.Client

\tdemo.Step("Connect to the fixture").
\t\tArrow("Host", "Server", "POST /mcp — initialize").
\t\tDashedArrow("Server", "Host", "serverInfo + capabilities + Mcp-Session-Id").
\t\tNote("Stub — refine in walkthrough.go.").
\t\tRun(func(ctx demokit.StepContext) *demokit.StepResult {{
\t\t\tc = client.NewClient(serverURL+"/mcp",
\t\t\t\tcore.ClientInfo{{Name: "{fixture}-host", Version: "1.0"}},
\t\t\t)
\t\t\tif err := c.Connect(); err != nil {{
\t\t\t\tfmt.Printf("    ERROR: %v\\n    Start the server with: make serve\\n", err)
\t\t\t\treturn nil
\t\t\t}}
\t\t\tfmt.Printf("    connected to %s %s\\n", c.ServerInfo.Name, c.ServerInfo.Version)
\t\t\treturn nil
\t\t}})

\tdemo.Step("List tools").
\t\tArrow("Host", "Server", "tools/list").
\t\tDashedArrow("Server", "Host", "tools[]").
\t\tNote("Stub — refine in walkthrough.go.").
\t\tRun(func(ctx demokit.StepContext) *demokit.StepResult {{
\t\t\tres, err := c.Call("tools/list", map[string]any{{}})
\t\t\tif err != nil {{
\t\t\t\tfmt.Printf("    ERROR: %v\\n", err)
\t\t\t\treturn nil
\t\t\t}}
\t\t\tpretty, _ := json.MarshalIndent(json.RawMessage(res.Raw), "    ", "  ")
\t\t\tfmt.Printf("    %s\\n", string(pretty))
\t\t\treturn nil
\t\t}})

\tcommon.SetupRenderer(demo)
\tdemo.Execute()
}}

// serverURLFor3101 returns the walkthrough's target MCP server URL.
// Honors $MCPKIT_SERVER_URL as an explicit override; defaults to
// localhost:3101 (the compat-fixture port).
func serverURLFor3101() string {{
\tif u := os.Getenv(common.ServerURLEnv); u != "" {{
\t\treturn u
\t}}
\treturn "http://localhost:3101"
}}
'''


DISPATCHER_INSERT = '''// Dual-mode dispatcher: `--demo` runs the demokit walkthrough (acts as
\t// an MCP client against a running server in another terminal). Default
\t// (no flag) keeps the existing server behaviour so apps_demo.py and
\t// the Playwright wrapper continue to work unchanged.
\tfor _, arg := range os.Args[1:] {
\t\tif strings.TrimSpace(arg) == "--demo" {
\t\t\trunDemo()
\t\t\treturn
\t\t}
\t}
\tserve()
}

func serve() {'''


def convert_main_to_dispatcher(main_go: str) -> str | None:
    """Renames `func main()` -> `func serve()` and inserts a new `main()`
    dispatcher that routes `--demo` to runDemo(). Returns None if main.go
    doesn't have a recognizable `func main() {` form (caller should skip).

    The function looks for the literal `func main() {` line and replaces
    the opening with the dispatcher + a renamed `func serve() {`. The
    body and closing brace stay untouched.
    """
    pat = re.compile(r"^func main\(\) \{\n", re.MULTILINE)
    m = pat.search(main_go)
    if not m:
        return None
    # Splice in the dispatcher.
    new = main_go[: m.start()] + "func main() {\n\t" + DISPATCHER_INSERT + "\n" + main_go[m.end():]
    # Ensure "strings" is in the import block.
    if '"strings"' not in new:
        new = re.sub(
            r"(\nimport \(\n)",
            r'\1\t"strings"\n',
            new,
            count=1,
        )
    return new


def binary_name(fixture: str) -> str:
    """Return the convention binary name: <fixture>-demo (matches the
    Makefile fragment's `go build -o $(FIXTURE_NAME)`)."""
    return f"{fixture}-demo"


def fixture_title(main_go: str, fixture: str) -> str:
    """Extract the upstream-facing fixture title from main.go's
    core.ServerInfo{Name: "..."} declaration. Falls back to the
    directory name if not found.
    """
    m = re.search(r'core\.ServerInfo\{\s*Name:\s*"([^"]+)"', main_go)
    if m:
        return m.group(1)
    return fixture


def bootstrap_fixture(fixture_dir: Path) -> tuple[bool, str]:
    fixture = fixture_dir.name
    if fixture in SKIP_FIXTURES:
        return False, "skip (in SKIP_FIXTURES)"
    if (fixture_dir / "walkthrough.go").exists():
        return False, "skip (walkthrough.go already exists)"

    main_go_path = fixture_dir / "main.go"
    if not main_go_path.exists():
        return False, "skip (no main.go)"

    main_go = main_go_path.read_text()
    converted = convert_main_to_dispatcher(main_go)
    if converted is None:
        return False, "skip (couldn't find `func main() {` to convert)"

    title = fixture_title(main_go, fixture)

    # Write all four files.
    main_go_path.write_text(converted)
    (fixture_dir / "walkthrough.go").write_text(
        WALKTHROUGH_STUB_TEMPLATE.format(title=title + " walkthrough (stub)", fixture=fixture)
    )
    (fixture_dir / "Makefile").write_text(MAKEFILE_BODY)
    (fixture_dir / ".gitignore").write_text(
        GITIGNORE_BODY_TEMPLATE.format(binary_name=binary_name(fixture))
    )
    return True, f"OK ({title})"


def main() -> int:
    fixture_dirs = sorted(p for p in COMPAT_ROOT.iterdir() if p.is_dir() and (p / "main.go").exists())
    if not fixture_dirs:
        print(f"ERROR: no fixture dirs under {COMPAT_ROOT}", file=sys.stderr)
        return 1
    n_ok = 0
    n_skipped = 0
    for d in fixture_dirs:
        ok, msg = bootstrap_fixture(d)
        prefix = "OK  " if ok else "SKIP"
        print(f"{prefix} {d.name:<28}  {msg}")
        if ok:
            n_ok += 1
        else:
            n_skipped += 1
    print(f"\n{n_ok} bootstrapped, {n_skipped} skipped")
    return 0


if __name__ == "__main__":
    sys.exit(main())
