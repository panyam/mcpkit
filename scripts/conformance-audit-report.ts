// conformance-audit-report.ts
//
// Reads the raw output dir produced by scripts/conformance-audit.sh and emits
// conformance/UPSTREAM_AUDIT.md — a per-SEP grouped report of mcpkit's
// status against every scenario in modelcontextprotocol/conformance@main.
//
// Invocation (from scripts/conformance-audit.sh):
//   npx tsx scripts/conformance-audit-report.ts <audit-out-dir> <report-path>
//
// The audit script runs `cd "$CONF_DIR" && npx tsx ...` so tsx can resolve from
// upstream's node_modules. We only depend on built-in Node APIs here — no
// upstream imports — to keep the script independent of upstream churn.

import { readFileSync, readdirSync, writeFileSync, existsSync, statSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { join, basename } from 'node:path';

// --- Schemas ----------------------------------------------------------------

interface SpecReference {
  id: string;
  url: string;
}

interface Check {
  id: string;
  name: string;
  description?: string;
  status: 'SUCCESS' | 'FAILURE' | 'WARNING' | string;
  errorMessage?: string;
  specReferences?: SpecReference[];
}

interface ScenarioResult {
  scenarioId: string;           // upstream scenario name (e.g., "server-ping")
  surface: 'server' | 'client'; // which side of the protocol
  dir: string;                   // absolute path on disk
  checks: Check[];               // parsed checks.json (empty if missing)
  hasResults: boolean;           // false if checks.json missing → harness-gap
  primarySEP: string | null;     // SEP-NNNN extracted from spec refs (first one wins)
  primarySEPUrl: string | null;
  specRefIds: string[];          // all spec refs for display
}

// --- Per-surface category overrides -----------------------------------------
//
// Some scenarios are already covered by an existing testconf-* fork target.
// Surface that explicitly so the audit doesn't make us double-prioritize.
//
// Keys are scenario IDs as upstream lists them; values are the fork target
// name that already grades this scenario.
const FORK_OVERLAP: Record<string, string> = {
  // SEP-2575 stateless wire — covered by testconf-stateless, which
  // drives examples/stateless via upstream's ServerStatelessScenario
  // (the scenario lives in upstream-main, no fork needed).
  'server-stateless': 'testconf-stateless',
};

// --- Spec URL overrides ------------------------------------------------------
//
// A few `specReferences[].url` strings in upstream scenario source point at
// files that no longer exist (404). We override them here so the audit's group
// headings link somewhere useful. Remove an entry when upstream fixes its
// reference. Keyed by the ref's `id` exactly as upstream emits it.
//
// Tracked upstream in modelcontextprotocol/conformance issue 313 (filed by us)
// with a fix PR from panyam:mcpconformance:fix/spec-references-404-urls.
// When that PR merges, drop the corresponding entry here:
//   - SEP-986: src/scenarios/server/tools.ts cites SEP/SEP-986.md which no
//     longer exists. Mirror upstream's proposed fix and point at issues/986
//     (matches the convention other SEPs in that file use).
//   - SEP-990-Enterprise-Managed-OAuth: src/scenarios/client/auth/
//     spec-references.ts cites enterprise-oauth.mdx; the actual file is
//     enterprise-managed-authorization.mdx.
const SPEC_URL_OVERRIDES: Record<string, string> = {
  'SEP-986':
    'https://github.com/modelcontextprotocol/modelcontextprotocol/issues/986',
  'SEP-990-Enterprise-Managed-OAuth':
    'https://github.com/modelcontextprotocol/ext-auth/blob/main/specification/draft/enterprise-managed-authorization.mdx',
};

// --- Parse one scenario directory -------------------------------------------

function parseScenarioDir(dir: string, surface: 'server' | 'client'): ScenarioResult {
  const dirName = basename(dir);

  // Dir names look like "<scenario-id>-2026-05-23T00-12-47-562Z". Strip the
  // ISO timestamp suffix (24 chars: -YYYY-MM-DDTHH-MM-SS-mmmZ).
  // Server scenarios are also CLI-prefixed with `server-`; strip that too.
  const TIMESTAMP_RE = /-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-\d{3}Z$/;
  let scenarioId = dirName.replace(TIMESTAMP_RE, '');
  if (surface === 'server' && scenarioId.startsWith('server-')) {
    scenarioId = scenarioId.slice('server-'.length);
  }

  const checksPath = join(dir, 'checks.json');
  let checks: Check[] = [];
  let hasResults = false;
  if (existsSync(checksPath)) {
    try {
      checks = JSON.parse(readFileSync(checksPath, 'utf-8'));
      hasResults = true;
    } catch (e) {
      // Malformed JSON — treat as harness-gap.
      hasResults = false;
    }
  }

  // Primary SEP = first specReference matching /^SEP-\d+/. Fall back to any
  // non-MCP ref, then null. We aggregate across all checks so a multi-check
  // scenario reports the SEP that ties it together.
  let primarySEP: string | null = null;
  let primarySEPUrl: string | null = null;
  const specRefSet = new Map<string, string>();
  for (const c of checks) {
    for (const r of c.specReferences ?? []) {
      const fixedUrl = SPEC_URL_OVERRIDES[r.id] ?? r.url;
      specRefSet.set(r.id, fixedUrl);
      if (!primarySEP && /^SEP-\d+/i.test(r.id)) {
        primarySEP = r.id.toUpperCase();
        primarySEPUrl = fixedUrl;
      }
    }
  }

  return {
    scenarioId,
    surface,
    dir,
    checks,
    hasResults,
    primarySEP,
    primarySEPUrl,
    specRefIds: Array.from(specRefSet.keys()),
  };
}

// --- Walk both surface dirs --------------------------------------------------

function collectScenarios(auditOut: string): ScenarioResult[] {
  const out: ScenarioResult[] = [];

  const serverDir = join(auditOut, 'server');
  if (existsSync(serverDir)) {
    for (const name of readdirSync(serverDir)) {
      const p = join(serverDir, name);
      if (statSync(p).isDirectory()) {
        out.push(parseScenarioDir(p, 'server'));
      }
    }
  }

  const clientDir = join(auditOut, 'client-auth');
  if (existsSync(clientDir)) {
    for (const name of readdirSync(clientDir)) {
      const p = join(clientDir, name);
      if (!statSync(p).isDirectory()) continue;
      // The `auth/` subdir holds the per-auth-scenario results when --suite
      // gates by `auth/*` prefix. Walk it one level deeper.
      if (name === 'auth') {
        for (const sub of readdirSync(p)) {
          const subP = join(p, sub);
          if (statSync(subP).isDirectory()) {
            const result = parseScenarioDir(subP, 'client');
            // Auth scenarios are prefixed `auth/` upstream; restore that.
            result.scenarioId = 'auth/' + result.scenarioId;
            out.push(result);
          }
        }
      } else {
        out.push(parseScenarioDir(p, 'client'));
      }
    }
  }

  return out;
}

// --- Categorize a scenario ---------------------------------------------------

type Category =
  | 'pass'              // all checks SUCCESS
  | 'partial'           // some pass, some fail
  | 'fail'              // all checks FAILURE
  | 'harness-gap'       // no checks.json (driver didn't produce results)
  | 'also-covered-by-fork';

function categorize(s: ScenarioResult): Category {
  if (!s.hasResults) return 'harness-gap';
  if (FORK_OVERLAP[s.scenarioId]) return 'also-covered-by-fork';
  const total = s.checks.length;
  if (total === 0) return 'harness-gap';
  const fail = s.checks.filter(c => c.status === 'FAILURE').length;
  const pass = s.checks.filter(c => c.status === 'SUCCESS').length;
  // INFO, SKIPPED, WARNING are non-failure; treat as "pass-equivalent" for the
  // scenario-level verdict (the per-check counts still surface them separately).
  if (fail === 0 && pass > 0) return 'pass';
  if (fail === 0 && pass === 0) return 'pass'; // all INFO/SKIPPED — not a failure
  if (fail === total) return 'fail';
  return 'partial';
}

// --- Aggregate counts --------------------------------------------------------

interface SurfaceStats {
  scenarios: number;
  checks: number;
  pass: number;
  fail: number;
  warning: number;
  info: number;
  skipped: number;
  harnessGap: number;
}

function statsFor(scenarios: ScenarioResult[]): SurfaceStats {
  let checks = 0, pass = 0, fail = 0, warning = 0, info = 0, skipped = 0, harnessGap = 0;
  for (const s of scenarios) {
    if (!s.hasResults) {
      harnessGap += 1;
      continue;
    }
    for (const c of s.checks) {
      checks += 1;
      if (c.status === 'SUCCESS') pass += 1;
      else if (c.status === 'FAILURE') fail += 1;
      else if (c.status === 'WARNING') warning += 1;
      else if (c.status === 'INFO') info += 1;
      else if (c.status === 'SKIPPED') skipped += 1;
    }
  }
  return { scenarios: scenarios.length, checks, pass, fail, warning, info, skipped, harnessGap };
}

// --- Group scenarios by primary SEP ------------------------------------------

interface SEPGroup {
  sep: string;             // "SEP-2549" or "Core / Unattributed"
  url: string | null;
  scenarios: ScenarioResult[];
}

function groupBySEP(scenarios: ScenarioResult[]): SEPGroup[] {
  const groups = new Map<string, SEPGroup>();
  for (const s of scenarios) {
    const key = s.primarySEP ?? 'Core / Unattributed';
    if (!groups.has(key)) {
      groups.set(key, { sep: key, url: s.primarySEPUrl, scenarios: [] });
    }
    groups.get(key)!.scenarios.push(s);
  }
  // Sort groups: Core first, then SEPs by number ascending.
  const ordered = Array.from(groups.values()).sort((a, b) => {
    if (a.sep === 'Core / Unattributed') return -1;
    if (b.sep === 'Core / Unattributed') return 1;
    const an = parseInt(a.sep.replace(/[^0-9]/g, ''), 10) || 0;
    const bn = parseInt(b.sep.replace(/[^0-9]/g, ''), 10) || 0;
    return an - bn;
  });
  // Sort scenarios within each group: failures first (most actionable), then partials, passes, harness-gaps.
  const catWeight: Record<Category, number> = {
    'fail': 0,
    'partial': 1,
    'harness-gap': 2,
    'also-covered-by-fork': 3,
    'pass': 4,
  };
  for (const g of ordered) {
    g.scenarios.sort((a, b) => {
      const w = catWeight[categorize(a)] - catWeight[categorize(b)];
      if (w !== 0) return w;
      return a.scenarioId.localeCompare(b.scenarioId);
    });
  }
  return ordered;
}

// --- Render markdown ---------------------------------------------------------

const STATUS_LABEL: Record<Category, string> = {
  'pass': 'pass',
  'partial': 'partial',
  'fail': 'fail',
  'harness-gap': 'harness-gap',
  'also-covered-by-fork': 'fork-covered',
};

function renderScenarioRow(s: ScenarioResult): string {
  const cat = categorize(s);
  const pass = s.checks.filter(c => c.status === 'SUCCESS').length;
  const fail = s.checks.filter(c => c.status === 'FAILURE').length;
  const warn = s.checks.filter(c => c.status === 'WARNING').length;
  const info = s.checks.filter(c => c.status === 'INFO').length;
  const skip = s.checks.filter(c => c.status === 'SKIPPED').length;
  const parts: string[] = [];
  if (pass) parts.push(`${pass} pass`);
  if (fail) parts.push(`${fail} fail`);
  if (warn) parts.push(`${warn} warn`);
  if (info) parts.push(`${info} info`);
  if (skip) parts.push(`${skip} skip`);
  const counts = s.hasResults ? (parts.join(' / ') || '0 checks') : '—';
  let note = '';
  if (cat === 'also-covered-by-fork') {
    note = `Also graded by \`${FORK_OVERLAP[s.scenarioId]}\``;
  } else if (cat === 'harness-gap') {
    note = 'No `checks.json` written — driver does not handle this scenario';
  } else if (cat === 'fail' && s.checks[0]?.errorMessage) {
    const msg = s.checks[0].errorMessage.replace(/\|/g, '\\|').replace(/`/g, '\'');
    note = '`' + msg.slice(0, 100) + (msg.length > 100 ? '…' : '') + '`';
  }
  return `| \`${s.scenarioId}\` | ${s.surface} | ${STATUS_LABEL[cat]} | ${counts} | ${note} |`;
}

function renderGroup(g: SEPGroup): string {
  const heading = g.sep === 'Core / Unattributed'
    ? `### Core / Unattributed (${g.scenarios.length} scenarios)`
    : `### [${g.sep}](${g.url ?? '#'}) (${g.scenarios.length} scenarios)`;
  return [
    heading,
    '',
    '| Scenario | Surface | Status | Checks | Note |',
    '|---|---|---|---|---|',
    ...g.scenarios.map(renderScenarioRow),
    '',
  ].join('\n');
}

function renderSummaryTable(server: SurfaceStats, client: SurfaceStats): string {
  return [
    '| Surface | Scenarios | Checks | Pass | Fail | Warn | Info | Skipped | Harness-gap |',
    '|---|---:|---:|---:|---:|---:|---:|---:|---:|',
    `| Server | ${server.scenarios} | ${server.checks} | ${server.pass} | ${server.fail} | ${server.warning} | ${server.info} | ${server.skipped} | ${server.harnessGap} |`,
    `| Client | ${client.scenarios} | ${client.checks} | ${client.pass} | ${client.fail} | ${client.warning} | ${client.info} | ${client.skipped} | ${client.harnessGap} |`,
    `| **Total** | **${server.scenarios + client.scenarios}** | **${server.checks + client.checks}** | **${server.pass + client.pass}** | **${server.fail + client.fail}** | **${server.warning + client.warning}** | **${server.info + client.info}** | **${server.skipped + client.skipped}** | **${server.harnessGap + client.harnessGap}** |`,
  ].join('\n');
}

function gitShow(cwd: string, args: string[]): string {
  try {
    return execFileSync('git', args, { cwd, encoding: 'utf-8' }).trim();
  } catch {
    return '';
  }
}

// --- Main --------------------------------------------------------------------

function main() {
  const [, , auditOut, reportPath] = process.argv;
  if (!auditOut || !reportPath) {
    console.error('usage: conformance-audit-report.ts <audit-out-dir> <report-path>');
    process.exit(2);
  }

  // Capture upstream commit for traceability.
  const upstreamBase = process.env.MCPCONFORMANCE_BASE_PATH ?? '';
  const upstreamSha = gitShow(upstreamBase, ['rev-parse', '--short', 'HEAD']) || 'unknown';
  const upstreamSubject = gitShow(upstreamBase, ['log', '-1', '--pretty=%s']);

  // Capture mcpkit commit too.
  const mcpkitSha = gitShow(process.cwd(), ['rev-parse', '--short', 'HEAD']) || 'unknown';

  const scenarios = collectScenarios(auditOut);
  const server = scenarios.filter(s => s.surface === 'server');
  const client = scenarios.filter(s => s.surface === 'client');

  const serverStats = statsFor(server);
  const clientStats = statsFor(client);

  const groups = groupBySEP(scenarios);

  const harnessGaps = scenarios.filter(s => !s.hasResults);

  // No `Audit run:` wall-clock — would create churn in the committed snapshot
  // every CI run. Upstream+mcpkit SHAs and scenario-status are the only header
  // fields that change with real content.
  const md = [
    '# Upstream Conformance Audit',
    '',
    `Snapshot of mcpkit graded against \`modelcontextprotocol/conformance@${upstreamSha}\` — *${upstreamSubject}*.`,
    '',
    `**mcpkit HEAD:** \`${mcpkitSha}\`  `,
    '**Driver:** `cmd/testserver` (server scenarios) + `cmd/testclient` (client scenarios). SEP-2663 `tasks-*` server scenarios are graded against `examples/tasks-v2` instead, which wires `ext/tasks` in its own module (keeping the root module free of that dependency) — mirroring how `testconf-stateless` uses `examples/stateless`.',
    '',
    'Informational report — not a CI gate. Regenerate via `make testconf-upstream-audit`.',
    '',
    'Status legend: **pass** = no FAILURE checks · **partial** = at least one SUCCESS and one FAILURE · **fail** = all checks FAILURE · **harness-gap** = no `checks.json` produced (driver missing) · **fork-covered** = same surface graded by an existing `testconf-*` SEP fork target.',
    '',
    '## Summary',
    '',
    renderSummaryTable(serverStats, clientStats),
    '',
    '## Harness gaps',
    '',
    harnessGaps.length === 0
      ? '_None — every scenario produced results._'
      : harnessGaps.map(s => `- \`${s.scenarioId}\` (${s.surface})`).join('\n'),
    '',
    '## By SEP',
    '',
    ...groups.map(renderGroup),
    '',
    '## Methodology',
    '',
    '- `make testconf-upstream-audit` spawns `cmd/testserver` (Streamable HTTP on port 18099), builds `cmd/testclient`, then drives upstream\'s CLI: `node dist/index.js server --url ... --suite all` once, and `... client --command ... --scenario <name>` per scenario in a loop (sequentially — upstream\'s parallel `--suite all` mode is flaky on the client side). The `tasks-*` server scenarios are then re-graded against `examples/tasks-v2` on a second port (port 18101), replacing the bulk-sweep results.',
    '- Upstream\'s CLI writes one `<scenario>/checks.json` per scenario; this report aggregates by `specReferences[]` (first matching `SEP-NNNN` wins as primary group).',
    '- Scenarios with no `checks.json` are tagged `harness-gap` — they require driver work in `cmd/testclient` (or a dedicated client harness) before the upstream runner can invoke them.',
    '- `also-covered-by-fork` is hand-maintained in `scripts/conformance-audit-report.ts` (`FORK_OVERLAP` map). Update there as SEP-fork targets land coverage.',
    '- Raw per-check JSON lives in `${AUDIT_OUT:-/tmp/conf-audit}/` — inspect there for failure details beyond the first 100 chars shown above.',
    '',
  ].join('\n');

  writeFileSync(reportPath, md);
  console.error(`Wrote ${reportPath} (${scenarios.length} scenarios, ${serverStats.checks + clientStats.checks} checks)`);
}

main();
