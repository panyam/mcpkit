#!/usr/bin/env python3
"""Write the /try/ landing page from the playground manifests (#1710).

scripts/collect_web_examples.sh copies each browser-ready example into
<try>/<example>/, including its web/playground.json. This script reads those
manifests and writes <try>/index.html with one card per example, so the page
lists exactly what was published.

Usage:
    python3 scripts/web_examples_index.py docs/site/dist/docs/try

Stdlib only. Exits 1 if a manifest is missing or lacks a title or summary.
"""

from __future__ import annotations

import html
import json
import sys
from pathlib import Path

# The site serves /try/ next to the rest of the docs, so docs links go up one.
SITE_ROOT = "../"
FIRST = "getting-started"

PAGE = """<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Try mcpkit in your browser</title>
<link rel="icon" href="data:,">
<style>
  :root {{
    --bg: #fafaf9; --fg: #1c1917; --muted: #78716c; --panel: #ffffff;
    --border: #e7e5e4; --accent: #2563eb;
  }}
  @media (prefers-color-scheme: dark) {{
    :root {{
      --bg: #0c0a09; --fg: #e7e5e4; --muted: #a8a29e; --panel: #1c1917;
      --border: #292524; --accent: #60a5fa;
    }}
  }}
  * {{ box-sizing: border-box; }}
  body {{ margin: 0; background: var(--bg); color: var(--fg);
         font: 15px/1.55 system-ui, -apple-system, sans-serif; }}
  main {{ max-width: 1000px; margin: 0 auto; padding: 32px 16px; }}
  nav {{ font-size: 13px; margin-bottom: 20px; }}
  nav a {{ color: var(--muted); text-decoration: none; }}
  h1 {{ font-size: 26px; margin: 0 0 8px; }}
  .lede {{ color: var(--muted); max-width: 700px; margin: 0 0 24px; }}
  .cards {{ display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 16px; }}
  .card {{ background: var(--panel); border: 1px solid var(--border); border-radius: 10px;
           padding: 16px; display: flex; flex-direction: column; }}
  .card h2 {{ font-size: 17px; margin: 0 0 6px; }}
  .card h2 a {{ color: var(--fg); text-decoration: none; }}
  .card p {{ margin: 0 0 14px; color: var(--muted); font-size: 14px; flex: 1; }}
  .links {{ display: flex; gap: 12px; align-items: center; font-size: 14px; flex-wrap: wrap; }}
  .links a {{ white-space: nowrap; }}
  .open {{ background: var(--accent); color: #fff; padding: 6px 12px; border-radius: 6px;
           text-decoration: none; font-weight: 600; }}
  .links a.docs {{ color: var(--accent); text-decoration: none; }}
  footer {{ margin-top: 32px; color: var(--muted); font-size: 13px; }}
</style>
</head>
<body>
<main>
  <nav><a href="{root}">mcpkit docs</a></nav>
  <h1>Try mcpkit in your browser</h1>
  <p class="lede">Each page runs a real mcpkit example server, compiled to WebAssembly, in a Web Worker
    in your tab. There is nothing to install and no backend. Call the tools with any input, answer the
    server when it asks you something, and watch every JSON-RPC message go back and forth.</p>
  <div class="cards">
{cards}
  </div>
  <footer>Each server downloads once, about 4 MB, then your browser caches it.
    The same code runs from the command line: every card links to its example.</footer>
</main>
</body>
</html>
"""

CARD = """    <div class="card" data-example="{name}">
      <h2><a href="{name}/">{title}</a></h2>
      <p>{summary}</p>
      <div class="links"><a class="open" href="{name}/">Open the playground</a><a class="docs" href="{docs}">Read the example</a></div>
    </div>"""


def load(try_dir: Path) -> list[tuple[str, dict]]:
    out = []
    for d in sorted(p for p in try_dir.iterdir() if p.is_dir()):
        manifest = d / "playground.json"
        if not manifest.is_file():
            sys.exit(f"web-examples-index: {d.name} has no playground.json")
        data = json.loads(manifest.read_text())
        for key in ("title", "summary"):
            if not data.get(key):
                sys.exit(f"web-examples-index: {d.name}/playground.json has no {key}")
        out.append((d.name, data))
    # getting-started leads, the rest go by title.
    out.sort(key=lambda e: (e[0] != FIRST, e[1]["title"].lower()))
    return out


def main() -> None:
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    try_dir = Path(sys.argv[1])
    examples = load(try_dir)
    cards = "\n".join(
        CARD.format(
            name=html.escape(name),
            title=html.escape(m["title"]),
            summary=html.escape(m["summary"]),
            docs=html.escape(SITE_ROOT + m.get("docs", "examples/")),
        )
        for name, m in examples
    )
    (try_dir / "index.html").write_text(PAGE.format(root=SITE_ROOT, cards=cards))
    print(f"web-examples-index: {len(examples)} card(s) -> {try_dir / 'index.html'}")


if __name__ == "__main__":
    main()
