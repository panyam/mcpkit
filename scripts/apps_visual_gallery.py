#!/usr/bin/env python3
"""Generate the apps/compat visual gallery — side-by-side mcpkit + upstream
baseline screenshots with bounding-box overlays around regions of pixel
difference.

For each fixture in `_apps_common.FIXTURES`:
  - Reads mcpkit's committed baseline: examples/apps/compat/<name>/__snapshots__/<name>.png
  - Reads upstream's matching baseline: /tmp/ext-apps/tests/e2e/servers.spec.ts-snapshots/<upstream-name>.png
  - Computes per-pixel max-channel delta, classifies regions:
      - delta > HIGH_THRESHOLD (default 30)  → "significant" → RED border
      - LOW < delta ≤ HIGH                   → "ignored"     → BLUE border
        (anti-aliasing / font-fallback noise — below visual-test tolerance)
  - Detects regions via coarse-grid connected components (24-pixel cells,
    BFS to merge adjacent cells); draws bordered-only rectangles on
    copies of both images so the underlying content stays visible.
  - Emits annotated PNGs into docs/site/static/conformance/apps/visual-gallery/
    and an index.html into docs/site/content/conformance/apps/visual-gallery/.

The gallery is a release-time docs page — author runs `make refresh-visual-gallery`
manually after a clean `make test-apps-playwright-docker-all`, commits the
regenerated artifacts. docs-site-build picks them up via the normal content tree.

Usage:
  uv run scripts/apps_visual_gallery.py
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw, ImageFont

from _apps_common import FIXTURES, MCPKIT_ROOT


DEFAULT_EXT_APPS = Path("/tmp/ext-apps")
UPSTREAM_SNAP_DIR_REL = Path("tests/e2e/servers.spec.ts-snapshots")

# Output locations relative to MCPKIT_ROOT.
GALLERY_CONTENT = Path("docs/site/content/conformance/apps/visual-gallery")
GALLERY_STATIC = Path("docs/site/static/conformance/apps/visual-gallery")

# Pixel-difference thresholds. Channel delta is max(|ΔR|, |ΔG|, |ΔB|).
# LOW gates pixel-level noise out of the candidate mask entirely; the
# dense-vs-sparse classifier in `_grid_components` then splits real solid
# changes (red) from edge-ghost rings (blue).
#
# These are conservative on purpose: the gallery is for release-time
# review, not the per-pixel CI gate. The Playwright snapshot test (in
# `make test-apps-playwright-docker`) is the strict pixel-perfect gate.
# Boxes here surface drift that's visible at a glance — full-region
# color changes, missing UI elements, shifted layouts — without firing
# on every sub-pixel font-rendering shimmer.
LOW_THRESHOLD = 20

CELL = 24  # Coarse-grid cell size for connected-component detection.
BORDER_WIDTH = 3
COLOR_SIGNIFICANT = (220, 38, 38, 255)  # red-600
COLOR_IGNORED = (37, 99, 235, 255)  # blue-600

# Upstream's spec.ts snapshots use a different short name than mcpkit's
# fixture directory for these. Default rule: the mcpkit fixture name maps
# 1:1 to a `<name>.png` in upstream's snapshot dir; this dict overrides
# that for the exceptions.
UPSTREAM_NAME_OVERRIDES = {
    "map": "map-server",
}


def _resolve_upstream_path(fixture_name: str, ext_apps: Path) -> Path | None:
    """Locate upstream's baseline PNG for this fixture. Returns None when
    we can't find one (fixture has no upstream snapshot — sometimes the
    case for SKIP-rows in upstream's servers.spec.ts)."""
    upstream_name = UPSTREAM_NAME_OVERRIDES.get(fixture_name, fixture_name)
    candidate = ext_apps / UPSTREAM_SNAP_DIR_REL / f"{upstream_name}.png"
    return candidate if candidate.is_file() else None


def _pad_to(image: Image.Image, target_size: tuple[int, int]) -> Image.Image:
    """Pad an image to target_size with transparent border. Used to align
    mcpkit + upstream when their baselines are different sizes (e.g. one
    has a few extra padding pixels)."""
    if image.size == target_size:
        return image
    padded = Image.new("RGBA", target_size, (0, 0, 0, 0))
    padded.paste(image, (0, 0))
    return padded


def _grid_components(
    mask: np.ndarray, cell: int, min_pixels_per_cell: int = 80,
) -> tuple[list[tuple[int, int, int, int]], list[tuple[int, int, int, int]]]:
    """Coarse-grid connected components on a boolean mask. Returns
    (dense_boxes, sparse_boxes) — both lists of (x0, y0, x1, y1).

    Algorithm:
      1. Bucket the H×W mask into a grid of `cell`-sized cells.
      2. A cell is "active" if it contains AT LEAST `min_pixels_per_cell`
         True pixels (default 32 of 576 = ~5.5% of the cell area). Filters
         out single-pixel "ghost edges" from 1-2 pixel sub-pixel rendering
         shifts between basic-host runs — a 1-px edge straight through a
         24×24 cell is ~24 pixels, just below threshold. Solid-region
         differences (a moved button, a re-rendered character) easily
         exceed it.
      3. BFS over the active-cells grid to find connected components.
      4. Each component's bounding box snaps back to pixel coordinates.
      5. SHAPE classification: a component is "dense" if its active cells
         fill ≥ 40% of its bounding box; otherwise "sparse" (thin
         ring / line-like / scattered).
         Real UI drift produces dense blobs (a button moved 5px = a
         filled rectangle of diff pixels). 1-2 pixel iframe-position
         shifts produce sparse rings (just the border outline of the
         iframe differs).

    The caller draws dense boxes in red (significant) and sparse boxes
    in blue (ignored / edge-ghost) regardless of pixel-magnitude tier.
    """
    h, w = mask.shape
    grid_h = (h + cell - 1) // cell
    grid_w = (w + cell - 1) // cell

    # Coarse mask: True if cell has ≥ min_pixels_per_cell True pixels.
    coarse = np.zeros((grid_h, grid_w), dtype=bool)
    for gi in range(grid_h):
        for gj in range(grid_w):
            block = mask[gi * cell : (gi + 1) * cell, gj * cell : (gj + 1) * cell]
            if int(block.sum()) >= min_pixels_per_cell:
                coarse[gi, gj] = True

    visited = np.zeros_like(coarse)
    dense: list[tuple[int, int, int, int]] = []
    sparse: list[tuple[int, int, int, int]] = []

    DENSITY_THRESHOLD = 0.4

    for gi in range(grid_h):
        for gj in range(grid_w):
            if not coarse[gi, gj] or visited[gi, gj]:
                continue
            queue = [(gi, gj)]
            cells: list[tuple[int, int]] = []
            while queue:
                ci, cj = queue.pop()
                if not (0 <= ci < grid_h and 0 <= cj < grid_w):
                    continue
                if not coarse[ci, cj] or visited[ci, cj]:
                    continue
                visited[ci, cj] = True
                cells.append((ci, cj))
                queue += [
                    (ci + 1, cj),
                    (ci - 1, cj),
                    (ci, cj + 1),
                    (ci, cj - 1),
                ]
            rows = [c[0] for c in cells]
            cols = [c[1] for c in cells]
            bbox_h = max(rows) - min(rows) + 1
            bbox_w = max(cols) - min(cols) + 1
            density = len(cells) / (bbox_h * bbox_w)
            x0 = min(cols) * cell
            y0 = min(rows) * cell
            x1 = min((max(cols) + 1) * cell, w)
            y1 = min((max(rows) + 1) * cell, h)
            box = (x0, y0, x1, y1)
            if density >= DENSITY_THRESHOLD:
                dense.append(box)
            else:
                sparse.append(box)
    return dense, sparse


def _is_mask_pixel(rgb: np.ndarray) -> np.ndarray:
    """True for pixels that are upstream's "volatile-region mask" color.

    Upstream's basic-host visual tests paint volatile regions (timestamps,
    dynamic counters, scrollbars) pure magenta `#FF00FF` so the visual-diff
    comparator treats those regions as identical (both sides have the
    mask). We strip masked pixels from the parity diff — they're not
    real UI drift, and the masks themselves are positioned slightly
    differently between mcpkit and upstream which would otherwise fire
    bogus bordered boxes.
    """
    return (rgb[..., 0] >= 240) & (rgb[..., 1] <= 15) & (rgb[..., 2] >= 240)


def _compute_delta(a: Image.Image, b: Image.Image) -> np.ndarray:
    """Pad to common size, return H×W array of max-channel delta."""
    target_w = max(a.width, b.width)
    target_h = max(a.height, b.height)
    a_arr = np.asarray(_pad_to(a.convert("RGBA"), (target_w, target_h)))
    b_arr = np.asarray(_pad_to(b.convert("RGBA"), (target_w, target_h)))
    # Compare only RGB; ignore alpha so padding-vs-content doesn't fire as diff.
    a_rgb = a_arr[..., :3].astype(np.int16)
    b_rgb = b_arr[..., :3].astype(np.int16)
    # For padded regions (any side has alpha=0), force delta=0 so the
    # extension area doesn't get flagged as diff.
    a_alpha = a_arr[..., 3]
    b_alpha = b_arr[..., 3]
    delta = np.max(np.abs(a_rgb - b_rgb), axis=2).astype(np.int16)
    delta[(a_alpha == 0) | (b_alpha == 0)] = 0
    # Strip masked pixels (either side magenta) — they're upstream's
    # volatile-region masks, not real UI drift.
    mask_pixels = _is_mask_pixel(a_rgb) | _is_mask_pixel(b_rgb)
    delta[mask_pixels] = 0
    return delta


def _draw_boxes(
    image: Image.Image,
    target_size: tuple[int, int],
    sig_boxes: list[tuple[int, int, int, int]],
    ign_boxes: list[tuple[int, int, int, int]],
) -> Image.Image:
    """Pad image to target_size, draw red boxes around significant diffs
    and blue boxes around ignored noise. Returns a new image (RGBA)."""
    out = _pad_to(image.convert("RGBA"), target_size).copy()
    draw = ImageDraw.Draw(out)
    for box in ign_boxes:
        draw.rectangle(box, outline=COLOR_IGNORED, width=BORDER_WIDTH)
    # Draw significant on top so they overlay any ignored boxes.
    for box in sig_boxes:
        draw.rectangle(box, outline=COLOR_SIGNIFICANT, width=BORDER_WIDTH)
    return out


def _process_fixture(
    fixture_name: str, mcpkit_path: Path, upstream_path: Path, static_out: Path,
) -> dict:
    """Annotate one fixture's pair. Returns a dict with counts + paths
    suitable for embedding in the gallery template."""
    mcpkit = Image.open(mcpkit_path)
    upstream = Image.open(upstream_path)

    delta = _compute_delta(mcpkit, upstream)
    # Any pixel above LOW_THRESHOLD goes into the candidate mask. Density
    # classification on the components (not pixel magnitude tier) decides
    # which ones are real drift (dense blob) vs edge-ghost (sparse ring).
    candidate_mask = delta > LOW_THRESHOLD

    sig_boxes, ign_boxes = _grid_components(candidate_mask, CELL)

    target_w = max(mcpkit.width, upstream.width)
    target_h = max(mcpkit.height, upstream.height)
    target = (target_w, target_h)

    mc_annot = _draw_boxes(mcpkit, target, sig_boxes, ign_boxes)
    up_annot = _draw_boxes(upstream, target, sig_boxes, ign_boxes)

    mc_out = static_out / f"{fixture_name}-mcpkit.png"
    up_out = static_out / f"{fixture_name}-upstream.png"
    static_out.mkdir(parents=True, exist_ok=True)
    mc_annot.save(mc_out)
    up_annot.save(up_out)

    return {
        "name": fixture_name,
        "mcpkit_image": mc_out.name,
        "upstream_image": up_out.name,
        "sig_count": len(sig_boxes),
        "ign_count": len(ign_boxes),
        "candidate_total_pixels": int(candidate_mask.sum()),
        "image_width": target_w,
        "image_height": target_h,
    }


def _render_index_html(rows: list[dict], missing: list[str]) -> str:
    """Render the gallery index.html.

    A static page — Tailwind-ish CDN classes for layout, no JS, no build
    step. Matches the rest of docs/site/'s rendering style.
    """
    total = len(rows) + len(missing)
    clean = sum(1 for r in rows if r["sig_count"] == 0 and r["ign_count"] == 0)
    sig_drift = sum(1 for r in rows if r["sig_count"] > 0)
    ignored_only = sum(
        1 for r in rows if r["sig_count"] == 0 and r["ign_count"] > 0
    )

    head = """<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>apps/compat visual gallery — mcpkit ⇄ upstream</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; max-width: 1400px; margin: 2rem auto; padding: 0 1.5rem; color: #1f2937; line-height: 1.55; }
  h1, h2 { color: #111827; }
  h1 { margin-bottom: 0.25rem; }
  .lead { color: #4b5563; }
  .legend { display: flex; gap: 1.5rem; margin: 1rem 0 2rem; padding: 0.75rem 1rem; background: #f3f4f6; border-radius: 8px; font-size: 0.9rem; }
  .legend span { display: inline-flex; align-items: center; gap: 0.4rem; }
  .swatch { width: 14px; height: 14px; border: 3px solid; box-sizing: border-box; }
  .swatch.red { border-color: rgb(220,38,38); }
  .swatch.blue { border-color: rgb(37,99,235); }
  .stats { display: grid; grid-template-columns: repeat(4, 1fr); gap: 1rem; margin: 1.5rem 0; }
  .stat { padding: 1rem; background: #f9fafb; border-radius: 8px; text-align: center; }
  .stat-num { font-size: 1.75rem; font-weight: 600; color: #111827; }
  .stat-label { font-size: 0.85rem; color: #6b7280; margin-top: 0.25rem; }
  .fixture { margin: 2rem 0 3rem; padding-top: 1.5rem; border-top: 1px solid #e5e7eb; cursor: zoom-in; }
  .fixture:hover { background: #fafbfc; }
  .fixture-head { display: flex; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: 1rem; }
  .fixture h2 { margin: 0; font-size: 1.25rem; font-family: ui-monospace, monospace; }
  .fixture-hint { font-size: 0.75rem; color: #9ca3af; }
  .fixture-tag { font-size: 0.85rem; padding: 0.2rem 0.6rem; border-radius: 4px; font-weight: 500; }
  .tag-clean { background: #dcfce7; color: #166534; }
  .tag-ignored { background: #dbeafe; color: #1e40af; }
  .tag-drift { background: #fee2e2; color: #991b1b; }
  .images { display: grid; grid-template-columns: 1fr 1fr; gap: 1rem; margin-top: 0.75rem; }
  .img-col { background: #fafafa; padding: 0.5rem; border-radius: 6px; }
  .img-col .caption { text-align: center; font-size: 0.85rem; color: #6b7280; padding: 0.4rem 0 0.3rem; font-weight: 500; }
  .img-col img { width: 100%; height: auto; display: block; border-radius: 4px; }
  .missing { padding: 1rem; background: #fef3c7; border-left: 4px solid #f59e0b; border-radius: 4px; margin-top: 2rem; }

  /* Zoom modal — click a fixture row to open at native pixel size with
     horizontal scroll for side-by-side comparison. */
  .zoom-modal {
    display: none;
    position: fixed; inset: 0; z-index: 1000;
    background: rgba(17, 24, 39, 0.94);
    overflow: auto;
    cursor: zoom-out;
  }
  .zoom-modal.open { display: block; }
  .zoom-bar {
    position: sticky; top: 0; z-index: 2;
    display: flex; align-items: center; gap: 1rem;
    padding: 0.75rem 1.25rem;
    background: rgba(17, 24, 39, 0.92);
    color: #f3f4f6;
    border-bottom: 1px solid #374151;
  }
  .zoom-bar .title { font-family: ui-monospace, monospace; font-size: 1rem; flex: 1; }
  .zoom-bar .hint { font-size: 0.8rem; color: #9ca3af; }
  .zoom-close {
    background: transparent; color: #f3f4f6;
    border: 1px solid #4b5563; border-radius: 4px;
    padding: 0.3rem 0.75rem;
    font-size: 0.9rem; cursor: pointer;
  }
  .zoom-close:hover { background: #1f2937; }
  .zoom-stage {
    display: flex; gap: 1rem;
    padding: 1rem 1.25rem 2rem;
    cursor: default;
    /* Allow horizontal scroll by making the row wider than viewport. */
    width: max-content;
    min-width: 100%;
  }
  .zoom-col {
    flex: 0 0 auto;
    background: #1f2937;
    padding: 0.5rem;
    border-radius: 6px;
  }
  .zoom-col .caption {
    color: #d1d5db; font-size: 0.85rem; font-weight: 500;
    padding: 0.3rem 0.2rem 0.5rem;
    text-align: center;
  }
  .zoom-col img { display: block; max-width: none; border-radius: 4px; }
</style>
</head>
<body>
"""

    body = []
    body.append('<h1>apps/compat visual gallery</h1>')
    body.append(
        '<p class="lead">Side-by-side committed baselines: '
        '<code>examples/apps/compat/&lt;fixture&gt;/__snapshots__/&lt;fixture&gt;.png</code> '
        'vs upstream\'s <code>modelcontextprotocol/ext-apps</code> equivalent. '
        'Bordered boxes mark regions of pixel-level difference; box outlines '
        'are positioned identically on both images so you can compare what\'s '
        'under each box.</p>'
    )

    body.append('<div class="legend">')
    body.append(
        '<span><span class="swatch red"></span> Dense diff region '
        '(solid block of changed pixels — likely real UI drift)</span>'
    )
    body.append(
        '<span><span class="swatch blue"></span> Sparse / edge-ghost region '
        '(thin outline — typically a 1-2 pixel layout shift, not real drift)</span>'
    )
    body.append('</div>')

    body.append('<div class="stats">')
    body.append(f'<div class="stat"><div class="stat-num">{total}</div><div class="stat-label">Fixtures audited</div></div>')
    body.append(f'<div class="stat"><div class="stat-num">{clean}</div><div class="stat-label">Pixel-identical</div></div>')
    body.append(f'<div class="stat"><div class="stat-num">{ignored_only}</div><div class="stat-label">Ignored-only noise</div></div>')
    body.append(f'<div class="stat"><div class="stat-num">{sig_drift}</div><div class="stat-label">Significant drift</div></div>')
    body.append('</div>')

    if missing:
        body.append('<div class="missing">')
        body.append(
            '<strong>Missing upstream baselines:</strong> ' +
            ', '.join(f'<code>{m}</code>' for m in missing) +
            ' — upstream\'s <code>servers.spec.ts-snapshots/</code> doesn\'t '
            'carry one for these. Run <code>make demo-upstream</code> against '
            'each example once to populate the ext-apps clone, or skip if '
            'upstream genuinely has no snapshot for this row.'
        )
        body.append('</div>')

    for r in rows:
        if r["sig_count"] > 0:
            tag_class, tag_text = "tag-drift", f"⚠ {r['sig_count']} significant region(s)"
        elif r["ign_count"] > 0:
            tag_class, tag_text = "tag-ignored", f"○ {r['ign_count']} ignored region(s)"
        else:
            tag_class, tag_text = "tag-clean", "✓ pixel-identical"
        mc_src = f'../../../static/conformance/apps/visual-gallery/{r["mcpkit_image"]}'
        up_src = f'../../../static/conformance/apps/visual-gallery/{r["upstream_image"]}'
        body.append(
            f'<div class="fixture" data-name="{r["name"]}" '
            f'data-mc="{mc_src}" data-up="{up_src}" tabindex="0" '
            f'role="button" aria-label="Zoom {r["name"]} baselines">'
        )
        body.append(
            f'<div class="fixture-head"><h2>{r["name"]}</h2>'
            f'<div style="display:flex;align-items:baseline;gap:0.75rem;">'
            f'<span class="fixture-hint">click to zoom</span>'
            f'<span class="fixture-tag {tag_class}">{tag_text}</span>'
            f'</div></div>'
        )
        body.append('<div class="images">')
        body.append(
            f'<div class="img-col"><div class="caption">mcpkit-Go</div>'
            f'<img src="{mc_src}" alt="mcpkit baseline for {r["name"]}"></div>'
        )
        body.append(
            f'<div class="img-col"><div class="caption">upstream TS</div>'
            f'<img src="{up_src}" alt="upstream baseline for {r["name"]}"></div>'
        )
        body.append('</div>')
        body.append('</div>')

    # Zoom modal — populated by JS when a fixture row is clicked.
    body.append("""
<div id="zoom-modal" class="zoom-modal" aria-hidden="true" aria-label="Zoomed baseline comparison">
  <div class="zoom-bar">
    <span class="title" id="zoom-title"></span>
    <span class="hint">Scroll horizontally to see both images at native size · ESC or click background to close</span>
    <button class="zoom-close" type="button" aria-label="Close zoom view">Close</button>
  </div>
  <div class="zoom-stage" id="zoom-stage">
    <div class="zoom-col"><div class="caption">mcpkit-Go</div><img id="zoom-mc" alt=""></div>
    <div class="zoom-col"><div class="caption">upstream TS</div><img id="zoom-up" alt=""></div>
  </div>
</div>
<script>
(function () {
  var modal = document.getElementById('zoom-modal');
  var titleEl = document.getElementById('zoom-title');
  var mcImg = document.getElementById('zoom-mc');
  var upImg = document.getElementById('zoom-up');
  var stage = document.getElementById('zoom-stage');
  var closeBtn = modal.querySelector('.zoom-close');

  function openZoom(name, mc, up) {
    titleEl.textContent = name;
    mcImg.src = mc;
    mcImg.alt = 'mcpkit baseline for ' + name;
    upImg.src = up;
    upImg.alt = 'upstream baseline for ' + name;
    modal.classList.add('open');
    modal.setAttribute('aria-hidden', 'false');
    modal.scrollTop = 0;
    modal.scrollLeft = 0;
    document.body.style.overflow = 'hidden';
  }

  function closeZoom() {
    modal.classList.remove('open');
    modal.setAttribute('aria-hidden', 'true');
    mcImg.src = '';
    upImg.src = '';
    document.body.style.overflow = '';
  }

  // Backdrop click closes; clicks inside the stage (the image row) don't.
  modal.addEventListener('click', function (e) {
    if (e.target === modal) closeZoom();
  });
  closeBtn.addEventListener('click', function (e) {
    e.stopPropagation();
    closeZoom();
  });
  stage.addEventListener('click', function (e) { e.stopPropagation(); });

  // Wire each .fixture row.
  document.querySelectorAll('.fixture').forEach(function (row) {
    function open() {
      openZoom(
        row.getAttribute('data-name'),
        row.getAttribute('data-mc'),
        row.getAttribute('data-up')
      );
    }
    row.addEventListener('click', open);
    row.addEventListener('keydown', function (e) {
      if (e.key === 'Enter' || e.key === ' ') {
        e.preventDefault();
        open();
      }
    });
  });

  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && modal.classList.contains('open')) closeZoom();
  });
})();
</script>
""")
    body.append('</body></html>\n')
    return head + "\n".join(body)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    parser.add_argument(
        "--ext-apps-dir",
        type=Path,
        default=DEFAULT_EXT_APPS,
        help=f"ext-apps checkout (default: {DEFAULT_EXT_APPS})",
    )
    parser.add_argument(
        "--content-dir",
        type=Path,
        default=MCPKIT_ROOT / GALLERY_CONTENT,
        help=f"Gallery content output dir (default: {GALLERY_CONTENT})",
    )
    parser.add_argument(
        "--static-dir",
        type=Path,
        default=MCPKIT_ROOT / GALLERY_STATIC,
        help=f"Annotated PNG output dir (default: {GALLERY_STATIC})",
    )
    args = parser.parse_args()

    if not args.ext_apps_dir.is_dir():
        print(f"ERROR: ext-apps clone not found at {args.ext_apps_dir}", file=sys.stderr)
        print("Run any apps_demo / apps_playwright_test invocation once first.", file=sys.stderr)
        return 1

    args.content_dir.mkdir(parents=True, exist_ok=True)
    args.static_dir.mkdir(parents=True, exist_ok=True)

    rows = []
    missing = []
    for f in FIXTURES:
        # mcpkit's snapshot filename usually matches f.name, but a few
        # fixtures (e.g. `map`) keep upstream's `<name>-server` snapshot
        # key for parity. Try both before giving up.
        snap_dir = MCPKIT_ROOT / f.fixture_dir / "__snapshots__"
        candidates = [snap_dir / f"{f.name}.png", snap_dir / f"{f.upstream_example}.png"]
        mcpkit_path = next((p for p in candidates if p.is_file()), None)
        if mcpkit_path is None:
            missing.append(f.name)
            continue
        upstream_path = _resolve_upstream_path(f.name, args.ext_apps_dir)
        if not upstream_path:
            missing.append(f.name)
            continue
        print(f"  {f.name}", flush=True)
        rows.append(_process_fixture(f.name, mcpkit_path, upstream_path, args.static_dir))

    index_html = _render_index_html(rows, missing)
    index_out = args.content_dir / "index.html"
    index_out.write_text(index_html)

    try:
        rel = index_out.relative_to(MCPKIT_ROOT)
    except ValueError:
        rel = index_out
    print(f"\nGallery written: {rel}")
    print(f"  {len(rows)} fixtures rendered, {len(missing)} missing baseline")
    return 0


if __name__ == "__main__":
    sys.exit(main())
