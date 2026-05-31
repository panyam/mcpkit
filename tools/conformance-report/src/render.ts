import type {
  ConformanceResult,
  ConformanceScenarioResult,
  KnownGapEntry,
  RenderInput,
  TraceabilityManifest
} from './types.js';

// BEGIN_GENERATED / END_GENERATED bracket the renderer-owned region of
// CONFORMANCE.md. Hand-edited content above BEGIN is preserved across
// regenerations; everything between the markers is replaced.
export const BEGIN_GENERATED = '<!-- begin:generated -->';
export const END_GENERATED = '<!-- end:generated -->';

// renderGeneratedBlock produces the renderer-owned region (stamp + summary +
// per-SEP requirements + gaps). The caller is responsible for splicing it
// into the surrounding hand-edited document via spliceGeneratedRegion.
//
// Two-table design intentionally avoids claiming a scenario→SEP join the
// upstream JSON does not support. Per the traceability manifest's own scope
// note ("tier-check reports at scenario granularity and does not currently
// expose per-check IDs, while this manifest carries check IDs but not
// scenario names"), the join cannot be done today. The summary table grades
// the suite at the wire level; the SEP table grades requirement coverage at
// the spec level. Together they give a faithful picture without inventing
// data.
export function renderGeneratedBlock(input: RenderInput): string {
  const lines: string[] = [];
  lines.push(BEGIN_GENERATED);
  lines.push(renderStamp(input));
  lines.push('');
  lines.push('## Conformance Summary');
  lines.push('');
  lines.push(...renderSummary(input));
  lines.push('');
  lines.push('## SEP Coverage');
  lines.push('');
  lines.push(...renderSepTable(input));
  lines.push('');
  lines.push('## Open Gaps');
  lines.push('');
  lines.push(...renderGaps(input));
  lines.push(END_GENERATED);
  return lines.join('\n');
}

// spliceGeneratedRegion replaces the BEGIN/END region in `existing` with
// `block`. If no region is present, appends `block` after the existing
// content (with a separating blank line). This preserves hand-edited
// content above the generated region across regenerations.
//
// Markers are matched as whole lines (preceded by newline or start-of-file,
// followed by newline or end-of-file). This is intentional: a maintainer
// writing the literal marker text inside the hand-edited Overview prose
// (e.g. documenting the marker convention) must not collide with the splice
// anchor. The whole-line constraint makes prose embeddings unambiguously
// safe.
export function spliceGeneratedRegion(
  existing: string | null,
  block: string
): string {
  const trailing = block.endsWith('\n') ? block : block + '\n';
  if (existing === null || existing === '') {
    return trailing;
  }
  const beginIdx = findStandaloneMarker(existing, BEGIN_GENERATED);
  const endIdx = findStandaloneMarker(existing, END_GENERATED);
  if (beginIdx === -1 || endIdx === -1 || endIdx < beginIdx) {
    const sep = existing.endsWith('\n') ? '\n' : '\n\n';
    return existing + sep + trailing;
  }
  const before = existing.slice(0, beginIdx);
  const after = existing.slice(endIdx + END_GENERATED.length);
  const tail = after.startsWith('\n') ? after : '\n' + after;
  return before + block + tail;
}

// findStandaloneMarker returns the byte offset of `marker` only when it sits
// on its own line (start-of-file or newline before; newline or end-of-file
// after). Returns -1 otherwise. Prevents prose collisions with the splice
// anchor.
function findStandaloneMarker(text: string, marker: string): number {
  let from = 0;
  while (from <= text.length) {
    const idx = text.indexOf(marker, from);
    if (idx === -1) return -1;
    const okBefore = idx === 0 || text[idx - 1] === '\n';
    const endOfMarker = idx + marker.length;
    const okAfter = endOfMarker === text.length || text[endOfMarker] === '\n';
    if (okBefore && okAfter) return idx;
    from = idx + 1;
  }
  return -1;
}

function renderStamp(input: RenderInput): string {
  return `<!-- generated against upstream-conformance@${input.upstreamSha} · protocol ${input.protocolVersion} · regenerate via scripts/refresh-conformance.sh -->`;
}

// --- Conformance summary ----------------------------------------------------
//
// Aggregates server and client scenario results at the wire level. This is
// the slice tier-check JSON does support; per-SEP attribution lives in the
// next section, sourced from traceability.

function renderSummary(input: RenderInput): string[] {
  const s = input.scorecard.checks.conformance;
  const c = input.scorecard.checks.client_conformance;
  const lines: string[] = [];
  lines.push('| Surface | Scenarios pass/total | Checks pass/fail |');
  lines.push('|---|---:|---:|');
  lines.push(
    `| Server | ${s.passed}/${s.total} | ${sumChecks(s, 'passed')}/${sumChecks(s, 'failed')} |`
  );
  lines.push(
    `| Client | ${c.passed}/${c.total} | ${sumChecks(c, 'passed')}/${sumChecks(c, 'failed')} |`
  );
  return lines;
}

function sumChecks(r: ConformanceResult, side: 'passed' | 'failed'): number {
  let total = 0;
  for (const d of r.details) {
    total += side === 'passed' ? d.checks_passed : d.checks_failed;
  }
  return total;
}

// --- SEP coverage -----------------------------------------------------------

function renderSepTable(input: RenderInput): string[] {
  const sepIds = Object.keys(input.traceability.seps).sort((a, b) => Number(a) - Number(b));
  if (sepIds.length === 0) {
    return ['_No SEPs declared in upstream traceability manifest._'];
  }
  const lines: string[] = [];
  lines.push('| SEP | Tested reqs | Excluded | Untested | Status |');
  lines.push('|---|---:|---:|---:|---|');
  for (const sep of sepIds) {
    const s = input.traceability.seps[sep];
    const label = s.specUrl ? `[SEP-${sep}](${s.specUrl})` : `SEP-${sep}`;
    const status =
      s.summary.untested === 0 && s.summary.tested > 0
        ? '**pass**'
        : s.summary.tested === 0
          ? '_untested_'
          : 'partial';
    lines.push(
      `| ${label} | ${s.summary.tested} | ${s.summary.excluded} | ${s.summary.untested} | ${status} |`
    );
  }
  lines.push('');
  lines.push(
    '_Status reflects upstream-declared requirements only. Scenario→SEP attribution is not exposed in tier-check JSON today; this column tracks "does upstream have a check ID for this SEP requirement", not "does mcpkit pass it". Per-SEP scenario pass/fail lives in `conformance/UPSTREAM_AUDIT.md`._'
  );
  return lines;
}

// --- Open gaps --------------------------------------------------------------

interface GapRow {
  scenario: string;
  surface: 'server' | 'client';
  checksFailed: number;
  checksPassed: number;
  annotation: KnownGapEntry | null;
}

function renderGaps(input: RenderInput): string[] {
  const fails: GapRow[] = [];
  for (const d of input.scorecard.checks.conformance.details) {
    if (!d.passed) {
      const normalized = normalizeScenarioId(d.scenario, 'server');
      fails.push({
        scenario: normalized,
        surface: 'server',
        checksFailed: d.checks_failed,
        checksPassed: d.checks_passed,
        annotation: lookupAnnotation(normalized, input.knownGaps)
      });
    }
  }
  for (const d of input.scorecard.checks.client_conformance.details) {
    if (!d.passed) {
      const normalized = normalizeScenarioId(d.scenario, 'client');
      fails.push({
        scenario: normalized,
        surface: 'client',
        checksFailed: d.checks_failed,
        checksPassed: d.checks_passed,
        annotation: lookupAnnotation(normalized, input.knownGaps)
      });
    }
  }
  fails.sort((a, b) => a.scenario.localeCompare(b.scenario));

  const untestedRequirements = collectUntestedRequirements(
    input.traceability,
    input.knownGaps
  );

  if (fails.length === 0 && untestedRequirements.length === 0) {
    return ['_No open conformance gaps._ \\o/'];
  }

  const lines: string[] = [];
  if (fails.length > 0) {
    lines.push('### Failing scenarios');
    lines.push('');
    lines.push('| Scenario | Surface | Checks fail/pass | Tracking |');
    lines.push('|---|---|---:|---|');
    for (const f of fails) {
      lines.push(
        `| \`${f.scenario}\` | ${f.surface} | ${f.checksFailed}/${f.checksPassed} | ${formatAnnotation(f.annotation)} |`
      );
    }
    lines.push('');
  }
  if (untestedRequirements.length > 0) {
    lines.push('### Declared requirements with no emitted check');
    lines.push('');
    lines.push('| SEP | Check ID | Tracking |');
    lines.push('|---|---|---|');
    for (const r of untestedRequirements) {
      lines.push(`| SEP-${r.sep} | \`${r.check}\` | ${formatAnnotation(r.annotation)} |`);
    }
  }
  return lines;
}

// normalizeScenarioId strips the timestamp suffix and (for server scenarios)
// the leading "server-" prefix that upstream tier-check pulls from output
// directory names. Mirrors the same fix-up scripts/conformance-audit-report.ts
// applies to its harness output. Example:
//   "server-tools-list-2026-05-31T00-07-40-860Z" → "tools-list".
const TIMESTAMP_RE = /-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-\d{3}Z$/;
function normalizeScenarioId(raw: string, surface: 'server' | 'client'): string {
  let id = raw.replace(TIMESTAMP_RE, '');
  if (surface === 'server' && id.startsWith('server-')) {
    id = id.slice('server-'.length);
  }
  return id;
}

function lookupAnnotation(
  scenario: string,
  knownGaps: { scenarios?: Record<string, KnownGapEntry> }
): KnownGapEntry | null {
  return knownGaps.scenarios?.[scenario] ?? null;
}

function lookupCheckAnnotation(
  checkId: string,
  knownGaps: { checks?: Record<string, KnownGapEntry> }
): KnownGapEntry | null {
  return knownGaps.checks?.[checkId] ?? null;
}

function formatAnnotation(a: KnownGapEntry | null): string {
  if (!a) return '—';
  const parts: string[] = [];
  if (a.issue) parts.push(a.issue);
  if (a.note) parts.push(a.note);
  return parts.length === 0 ? '—' : parts.join(' — ');
}

function collectUntestedRequirements(
  trace: TraceabilityManifest,
  knownGaps: { checks?: Record<string, KnownGapEntry> }
): Array<{ sep: string; check: string; annotation: KnownGapEntry | null }> {
  const out: Array<{ sep: string; check: string; annotation: KnownGapEntry | null }> = [];
  for (const sep of Object.keys(trace.seps).sort((a, b) => Number(a) - Number(b))) {
    const s = trace.seps[sep];
    for (const r of s.requirements) {
      if (r.status === 'untested') {
        out.push({
          sep,
          check: r.check,
          annotation: lookupCheckAnnotation(r.check, knownGaps)
        });
      }
    }
  }
  return out;
}
