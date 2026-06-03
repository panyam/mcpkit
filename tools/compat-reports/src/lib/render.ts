// Shared rendering primitives for extension-compat reports. Each
// extension's entry point composes these into its own report file.

import type { StatusRow } from './umbrella.js';

// Status keys used in the umbrella table. Extensions may extend this
// set; the badge lookup falls back to "⬜ <status>" for unknown values.
export type KnownStatus = 'OK' | 'WIP' | 'PROT' | 'SKIP' | 'NOT';

// StatusBadge returns the emoji-prefixed display string for a status.
// Unknown statuses fall back to a neutral square so rendering doesn't
// silently swallow a typo in the umbrella table.
export function statusBadge(status: string): string {
  switch (status) {
    case 'OK':   return '✅ OK';
    case 'WIP':  return '🟡 WIP';
    case 'PROT': return '🟡 PROT';
    case 'SKIP': return '⏭ SKIP';
    case 'NOT':  return '⬜ NOT';
    default:     return `⬜ ${status}`;
  }
}

// Tally is a per-status count plus implementation totals. Implemented
// = OK + WIP + PROT (everything except SKIP / NOT). Pass rate is OK
// over implemented; reported as "N / M" plus a rounded percent.
export interface Tally {
  total: number;
  ok: number;
  wip: number;     // WIP + PROT combined
  skip: number;
  not: number;
  implemented: number;
  passRate: string; // "100%" | "—"
}

export function tally(rows: StatusRow[]): Tally {
  let ok = 0, wip = 0, skip = 0, not = 0;
  for (const r of rows) {
    switch (r.status) {
      case 'OK':            ok++; break;
      case 'WIP': case 'PROT': wip++; break;
      case 'SKIP':          skip++; break;
      case 'NOT':           not++; break;
    }
  }
  const implemented = ok + wip;
  const passRate = implemented > 0
    ? `${Math.round((ok / implemented) * 100)}%`
    : '—';
  return {
    total: rows.length,
    ok, wip, skip, not,
    implemented,
    passRate,
  };
}

// renderCoverageTable produces the "## Coverage" section's metric table
// using the tally results.
export function renderCoverageTable(t: Tally): string {
  const lines = [
    '| Metric | Count |',
    '|---|---|',
    `| Total upstream examples | ${t.total} |`,
    `| Drift-strict parity (OK) | ${t.ok} |`,
    `| In progress (WIP / PROT) | ${t.wip} |`,
    `| Skipped by upstream's test set | ${t.skip} |`,
    `| Not yet implemented | ${t.not} |`,
    `| Pass rate over implemented | ${t.ok} / ${t.implemented} (${t.passRate}) |`,
  ];
  return lines.join('\n');
}

// renderStatusTable produces the per-fixture rows table. The notes
// column is passed through verbatim — any markdown inside (inline
// code, PR refs, links) renders as-is in the gh-pages output.
export function renderStatusTable(rows: StatusRow[]): string {
  const lines = [
    '| # | Fixture | Status | Detail |',
    '|---|---|---|---|',
  ];
  for (const r of rows) {
    lines.push(`| ${r.num} | ${r.name} | ${statusBadge(r.status)} | ${r.notes} |`);
  }
  return lines.join('\n');
}
