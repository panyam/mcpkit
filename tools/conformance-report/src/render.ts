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
  lines.push('## mcpkit-local Conformance Suites');
  lines.push('');
  lines.push(...renderLocalSuites(input));
  lines.push('');
  lines.push('## SEP Coverage');
  lines.push('');
  lines.push(...renderSepTable(input));
  lines.push('');
  lines.push('## SEP Detail');
  lines.push('');
  lines.push(...renderSepDetail(input));
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

// --- mcpkit-local conformance suites ----------------------------------------
//
// Renders the table sourced from conformance/local-suites.yaml. Rows are kept
// in the order declared in the YAML so the maintainer controls visual
// grouping. Notes attach as numbered footnotes below the table when present.
//
// Status semantics:
//   PASS - suite runs green today
//   FAIL - known failures block the umbrella
//   INFO - wired via run_stage_info, surfaces in testall without counting
//          toward PASS/FAIL totals (e.g. an in-flight fork-side suite)
//   SKIP - intentionally not run in this revision

function renderLocalSuites(input: RenderInput): string[] {
  const suites = input.localSuites.suites;
  if (suites.length === 0) {
    return [
      '_No mcpkit-local conformance suites declared._'
    ];
  }
  const lines: string[] = [];
  lines.push(
    'These suites exercise SEP-specific behavior beyond what upstream\'s tier-check covers. Each is wired into `just testall` as a separate stage and may show as PASS, FAIL, INFO (informational, not gating), or SKIP. INFO typically means "work in flight" — see the Tracking column. The Source column links to the branch the scenarios live on; per-suite env vars and default checkout paths are listed below the tables.'
  );
  const footnotes: string[] = [];
  const pushTable = (subset: typeof suites) => {
    lines.push('| Suite | Covers | Stage | Status | Source | Tracking |');
    lines.push('|---|---|:---:|:---:|---|---|');
    for (const s of subset) {
      let statusCell: string;
      switch (s.status) {
        case 'PASS':
          statusCell = '**PASS**';
          break;
        case 'FAIL':
          statusCell = '**FAIL**';
          break;
        case 'INFO':
          statusCell = '_INFO_';
          break;
        case 'SKIP':
          statusCell = '_SKIP_';
          break;
      }
      if (s.note) {
        const idx = footnotes.length + 1;
        statusCell = `${statusCell}<sup>${idx}</sup>`;
        footnotes.push(`<sup>${idx}</sup> ${s.note}`);
      }
      let sourceCell: string;
      if (s.source) {
        const url = `https://github.com/${s.source.repo}/tree/${s.source.branch}`;
        sourceCell = `[\`${s.source.repo}@${s.source.branch}\`](${url})`;
      } else {
        sourceCell = '—';
      }
      const tracking = s.tracking ? s.tracking : '—';
      lines.push(
        `| \`${s.suite}\` | ${s.sep} | ${s.stage} | ${statusCell} | ${sourceCell} | ${tracking} |`
      );
    }
  };
  // Split by scenario ownership so a reader can tell at a glance which
  // results are graded by upstream-maintained scenarios versus scenarios
  // this project authored (usually in the panyam/mcpconformance fork while
  // a SEP is still in flight upstream). A suite with no source block is
  // mcpkit-authored by definition.
  const upstreamSuites = suites.filter(
    (s) => s.source?.repo === 'modelcontextprotocol/conformance'
  );
  const forkSuites = suites.filter(
    (s) => s.source?.repo !== 'modelcontextprotocol/conformance'
  );
  if (upstreamSuites.length > 0) {
    lines.push('');
    lines.push('### Upstream-scenario suites');
    lines.push('');
    lines.push(
      '_Scenarios owned and maintained in `modelcontextprotocol/conformance`; mcpkit supplies only the fixture under test._'
    );
    lines.push('');
    pushTable(upstreamSuites);
  }
  if (forkSuites.length > 0) {
    lines.push('');
    lines.push('### mcpkit-authored suites');
    lines.push('');
    lines.push(
      '_Scenarios authored by this project, typically in the `panyam/mcpconformance` fork while the SEP they cover is still in flight upstream. They assert what the spec says, not what mcpkit does, but they have not been through upstream review._'
    );
    lines.push('');
    pushTable(forkSuites);
  }
  if (footnotes.length > 0) {
    lines.push('');
    for (const f of footnotes) {
      lines.push(f);
    }
  }

  // Per-suite setup detail: env var + default path. Lets a reader who wants
  // to actually run the suite see where to clone, what to clone, and what
  // env var to override if their worktree lives elsewhere. Listed only for
  // suites that declared a source block.
  const withSource = suites.filter((s) => s.source);
  if (withSource.length > 0) {
    lines.push('');
    lines.push('### Setup — clone the right worktree per suite');
    lines.push('');
    lines.push(
      "Each suite's Makefile target reads `MCPCONFORMANCE_*_PATH` to find its scenario worktree. Defaults assume sibling clones of the source repo at the relative path shown. Override per-invocation when the worktree lives elsewhere."
    );
    lines.push('');
    lines.push('| Suite | Env var | Default path | Clone command |');
    lines.push('|---|---|---|---|');
    for (const s of withSource) {
      const src = s.source!;
      const clone = `\`git clone -b ${src.branch} https://github.com/${src.repo}.git ${src.defaultPath}\``;
      lines.push(
        `| \`${s.suite}\` | \`${src.pathVar}\` | \`${src.defaultPath}\` | ${clone} |`
      );
    }
  }
  return lines;
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
    const tested = numericCell(s.summary.tested, sep, 'tested', summarizeTested(s));
    const excluded = numericCell(s.summary.excluded, sep, 'excluded', summarizeExcluded(s));
    const untested = numericCell(s.summary.untested, sep, 'untested', summarizeUntested(s));
    lines.push(`| ${label} | ${tested} | ${excluded} | ${untested} | ${status} |`);
  }
  lines.push('');
  lines.push(
    '_Numeric cells link to per-SEP detail below; hover/long-press surfaces a one-line summary. Status reflects upstream-declared requirements only — Scenario→SEP attribution is not exposed in tier-check JSON today; this column tracks "does upstream have a check ID for this SEP requirement", not "does mcpkit pass it". Per-SEP scenario pass/fail lives in `conformance/UPSTREAM_AUDIT.md`._'
  );
  return lines;
}

// numericCell renders an integer count as either a plain `0` (no anchor when
// there's nothing to link to) or a clickable markdown link of the form
// `[N](#sep-NNNN-status "tooltip text")`. On GitHub-flavored markdown this
// becomes a hover tooltip on desktop and a tap-to-jump anchor on mobile.
function numericCell(
  count: number,
  sep: string,
  status: 'tested' | 'excluded' | 'untested',
  tooltip: string
): string {
  if (count === 0) return '0';
  return `[${count}](#sep-${sep}-${status} "${escapeTitle(tooltip)}")`;
}

// escapeTitle keeps the title attribute parseable as markdown. Double-quotes
// would close the title; backslashes are escape characters in many markdown
// parsers. Newlines collapse to spaces in browser tooltip rendering, so we
// drop them too.
function escapeTitle(s: string): string {
  return s.replace(/[\\"]/g, '').replace(/\s+/g, ' ').trim();
}

// summarizeTested returns a short string for the hover tooltip on the
// "Tested reqs" cell — first three check IDs and an ellipsis if more.
function summarizeTested(s: TraceabilityManifest['seps'][string]): string {
  const ids = s.requirements
    .filter((r) => r.status === 'tested')
    .map((r) => r.check);
  if (ids.length === 0) return 'No tested requirements.';
  const shown = ids.slice(0, 3).join(', ');
  return ids.length <= 3 ? `Tested: ${shown}.` : `Tested: ${shown}, +${ids.length - 3} more.`;
}

// summarizeUntested returns the untested check IDs verbatim — they are
// almost always few enough to fit.
function summarizeUntested(s: TraceabilityManifest['seps'][string]): string {
  const ids = s.requirements
    .filter((r) => r.status === 'untested')
    .map((r) => r.check);
  if (ids.length === 0) return 'No untested requirements.';
  return `Untested: ${ids.join(', ')}.`;
}

// summarizeExcluded groups exclusions by reason and emits a "Nx <reason>"
// breakdown. Long reasons are truncated so the tooltip stays readable;
// the full text lives in the SEP Detail section below.
function summarizeExcluded(s: TraceabilityManifest['seps'][string]): string {
  if (s.excluded.length === 0) return 'No exclusions.';
  const tally = new Map<string, number>();
  for (const e of s.excluded) {
    const r = (e.reason || '(no reason given)').slice(0, 60);
    tally.set(r, (tally.get(r) ?? 0) + 1);
  }
  const parts = Array.from(tally.entries())
    .sort((a, b) => b[1] - a[1])
    .map(([reason, n]) => `${n}x ${reason}`);
  return parts.join('; ');
}

// --- SEP detail -------------------------------------------------------------
//
// One subsection per SEP that enumerates tested check IDs, excluded
// requirements (with reasons), and untested check IDs. The SEP Coverage
// table's numeric cells link here. The detail anchors are stable HTML
// `<a id="sep-NNNN-status">` so they survive markdown auto-id changes
// across renderers (GitHub, goldmark on the docs site, etc.).

function renderSepDetail(input: RenderInput): string[] {
  const sepIds = Object.keys(input.traceability.seps).sort((a, b) => Number(a) - Number(b));
  if (sepIds.length === 0) {
    return ['_No SEPs declared._'];
  }
  const lines: string[] = [];
  lines.push(
    'Per-SEP breakdown of upstream traceability — what is exercised, what is intentionally excluded, and what is declared but not yet exercised. Useful for auditing whether each exclusion still makes sense as upstream evolves. Check IDs link to their definition in the upstream SEP YAML.'
  );
  lines.push('');
  for (const sep of sepIds) {
    const s = input.traceability.seps[sep];
    lines.push(`### SEP-${sep}`);
    lines.push('');
    lines.push(...renderSepDetailBlock(sep, s, input.upstreamSha));
    lines.push('');
  }
  return lines;
}

// upstreamYamlUrl builds the GitHub blob URL for the upstream SEP YAML at
// the pinned conformance SHA. Returns null when traceability did not record
// a yaml path for this SEP (the manifest field is nullable).
function upstreamYamlUrl(yamlPath: string | null, sha: string): string | null {
  if (!yamlPath) return null;
  return `https://github.com/modelcontextprotocol/conformance/blob/${sha}/${yamlPath}`;
}

// linkCheck wraps a check ID in a markdown link to the upstream SEP YAML so
// the reader can jump straight to the scenario/check definition. Falls back
// to a plain code span when no yaml path is available.
function linkCheck(checkId: string, yamlUrl: string | null): string {
  if (!yamlUrl) return `\`${checkId}\``;
  return `[\`${checkId}\`](${yamlUrl})`;
}

function renderSepDetailBlock(
  sep: string,
  s: TraceabilityManifest['seps'][string],
  sha: string
): string[] {
  const out: string[] = [];
  const yamlUrl = upstreamYamlUrl(s.yaml, sha);

  const tested = s.requirements.filter((r) => r.status === 'tested');
  out.push(`<a id="sep-${sep}-tested"></a>`);
  out.push('');
  out.push(`**Tested (${tested.length})**`);
  out.push('');
  if (tested.length === 0) {
    out.push('_None._');
  } else {
    for (const r of tested) {
      out.push(`- ${linkCheck(r.check, yamlUrl)}`);
    }
  }
  out.push('');

  out.push(`<a id="sep-${sep}-excluded"></a>`);
  out.push('');
  out.push(`**Excluded (${s.excluded.length})**`);
  out.push('');
  if (s.excluded.length === 0) {
    out.push('_None._');
  } else {
    out.push('| Requirement | Upstream reason |');
    out.push('|---|---|');
    for (const e of s.excluded) {
      const reqText = inlineCell(e.text);
      const reasonAndIssue = e.issue
        ? `${inlineCell(e.reason || 'No reason given.')} (${inlineCell(e.issue)})`
        : inlineCell(e.reason || 'No reason given.');
      out.push(`| ${reqText} | ${reasonAndIssue} |`);
    }
  }
  out.push('');

  const untested = s.requirements.filter((r) => r.status === 'untested');
  out.push(`<a id="sep-${sep}-untested"></a>`);
  out.push('');
  out.push(`**Untested (${untested.length})**`);
  out.push('');
  if (untested.length === 0) {
    out.push('_None._');
  } else {
    for (const r of untested) {
      const detail = r.text ? ` — ${inlineCell(r.text)}` : '';
      out.push(`- ${linkCheck(r.check, yamlUrl)}${detail}`);
    }
  }

  return out;
}

// inlineCell collapses whitespace and escapes pipe characters so a
// requirement quote stays on one table row without breaking the markdown
// table's column boundaries.
function inlineCell(s: string): string {
  return s.replace(/\s+/g, ' ').replace(/\|/g, '\\|').trim();
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
