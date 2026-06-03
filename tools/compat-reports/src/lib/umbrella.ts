// Shared utilities for fetching + parsing an extension-compat tracking
// umbrella issue. Each MCP extension that ships a drop-in compat suite
// (apps, tasks-v2, mrtr, ...) keeps its per-fixture status in a single
// hand-maintained GitHub issue. These helpers turn that issue body into
// typed rows the per-extension renderers can format.

import { execFileSync } from 'node:child_process';

// StatusRow is one row of the `## Status` table in an umbrella issue.
// The umbrella convention is:
//   | # | name | status | notes |
//
// Any markdown inside `notes` (inline code, links, PR refs) is preserved
// verbatim and rendered by the page consumer — we do not parse it.
export interface StatusRow {
  num: string;
  name: string;
  status: string;
  notes: string;
}

// FetchOptions controls how the umbrella body is obtained. The default
// is to shell out to `gh` because that's already a project-wide
// dependency and survives both interactive (gh auth) and CI (GH_TOKEN)
// auth modes uniformly. Callers can inject a `body` directly for
// testing — that bypass keeps the parser logic independent of network.
export interface FetchOptions {
  umbrellaNumber: number;
  repo: string;
  // If set, skip the gh CLI call and use this string verbatim.
  bodyOverride?: string;
}

// fetchUmbrellaBody resolves the umbrella issue body. Throws on:
//   - gh CLI missing
//   - empty body (issue doesn't exist or returned no content)
// Both conditions are caller errors we want surfaced loudly rather
// than silently rendering an empty report.
export function fetchUmbrellaBody(opts: FetchOptions): string {
  if (opts.bodyOverride !== undefined) {
    return opts.bodyOverride;
  }
  const body = execFileSync(
    'gh',
    [
      'issue', 'view', String(opts.umbrellaNumber),
      '--repo', opts.repo,
      '--json', 'body',
      '--jq', '.body',
    ],
    {
      encoding: 'utf-8',
      // EMU accounts can't read personal repos with their org token.
      // GH_PERSONAL_TOKEN (when set) takes precedence over GH_TOKEN —
      // matches the project-wide convention from scripts/*.sh.
      env: {
        ...process.env,
        GH_TOKEN: process.env.GH_PERSONAL_TOKEN || process.env.GH_TOKEN || '',
      },
    },
  ).trim();
  if (!body) {
    throw new Error(
      `Empty body for ${opts.repo}#${opts.umbrellaNumber} — verify the issue exists and gh auth works.`,
    );
  }
  return body;
}

// parseStatusTable extracts table rows from the `## Status` section of
// an umbrella issue body. Strategy: linear scan, enter the section on
// the `## Status` heading and leave it on the next `## ` heading. Body
// rows are recognized by `| <num> |` prefix; this skips the header row
// (`| # |`) and the separator row (`|---|...|`).
//
// Returns rows in document order. Empty result means the section is
// missing or the table is empty — callers decide whether that's an
// error (it almost always is).
export function parseStatusTable(body: string): StatusRow[] {
  const rows: StatusRow[] = [];
  let inSection = false;
  for (const line of body.split(/\r?\n/)) {
    if (/^## Status\b/.test(line)) {
      inSection = true;
      continue;
    }
    if (inSection && /^## /.test(line)) {
      break;
    }
    if (!inSection) continue;
    // Body row guard: starts with `| ` followed by a digit. Excludes
    // the header (`| #`) and the separator (`|---`).
    if (!/^\|\s*\d+\s*\|/.test(line)) continue;
    const cells = splitTableRow(line);
    if (cells.length < 4) continue;
    rows.push({
      num: cells[0],
      name: cells[1],
      status: cells[2],
      notes: cells[3],
    });
  }
  return rows;
}

// splitTableRow strips leading + trailing pipes and splits on `|`,
// preserving any internal markdown (inline code, `\`pipes-in-code\``
// would break this but the umbrella convention is plain prose so we
// don't pay that cost). Trim each cell.
function splitTableRow(line: string): string[] {
  const trimmed = line.trim().replace(/^\|/, '').replace(/\|$/, '');
  return trimmed.split('|').map((c) => c.trim());
}
