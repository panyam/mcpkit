#!/usr/bin/env node
// CLI entry for the apps-compat report. Fetches umbrella issue 533 (by
// default), parses its status table, and renders conformance/apps/COMPAT.md.
//
// Invocation (via scripts/refresh-apps-compat-report.sh):
//   npx tsx tools/compat-reports/src/apps.ts \
//     --umbrella 533 \
//     --repo panyam/mcpkit \
//     --out conformance/apps/COMPAT.md
//
// Deterministic contract: re-running on an unchanged umbrella body
// produces a byte-identical file. The check-apps-compat-stale CI gate
// enforces refresh + git diff --exit-code on PRs that touch
// examples/apps/compat/**.

import { writeFileSync } from 'node:fs';
import { fetchUmbrellaBody, parseStatusTable } from './lib/umbrella.js';
import {
  renderCoverageTable,
  renderStatusTable,
  tally,
} from './lib/render.js';

interface CliArgs {
  umbrella: number;
  repo: string;
  out: string;
}

function parseArgs(argv: string[]): CliArgs {
  let umbrella = 533;
  let repo = 'panyam/mcpkit';
  let out = 'conformance/apps/COMPAT.md';
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    const next = (): string => {
      const v = argv[++i];
      if (v === undefined) throw new Error(`${arg} requires a value`);
      return v;
    };
    switch (arg) {
      case '--umbrella': umbrella = Number(next()); break;
      case '--repo':     repo = next(); break;
      case '--out':      out = next(); break;
      case '-h': case '--help':
        printHelpAndExit();
    }
  }
  if (!Number.isFinite(umbrella) || umbrella <= 0) {
    throw new Error(`--umbrella must be a positive integer (got ${umbrella})`);
  }
  return { umbrella, repo, out };
}

function printHelpAndExit(): never {
  process.stderr.write(`Usage: apps-compat-report [options]

Options:
  --umbrella <num>   GitHub issue number for the umbrella (default: 533)
  --repo <owner/n>   Repo holding the umbrella (default: panyam/mcpkit)
  --out <path>       Output markdown path (default: conformance/apps/COMPAT.md)
  -h, --help         Show this help

Auth: uses GH_PERSONAL_TOKEN if set, falling back to GH_TOKEN.
`);
  process.exit(2);
}

function renderReport(args: CliArgs): string {
  const body = fetchUmbrellaBody({
    umbrellaNumber: args.umbrella,
    repo: args.repo,
  });
  const rows = parseStatusTable(body);
  if (rows.length === 0) {
    throw new Error(
      `No status rows parsed from ${args.repo}#${args.umbrella} — ` +
      `verify the issue has a '## Status' section with a markdown table.`,
    );
  }
  const t = tally(rows);
  return `# Apps Compat — mcpkit ⇄ ext-apps

mcpkit-Go drop-in replacements for upstream's
[\`modelcontextprotocol/ext-apps\`](https://github.com/modelcontextprotocol/ext-apps)
Playwright suite. Each fixture under \`examples/apps/compat/<name>/\`
exposes the same MCP tool surface as the upstream TypeScript example,
serves upstream's verbatim \`dist/mcp-app.html\`, and is verified
end-to-end by upstream's own Playwright tests run against the Go
binary via \`basic-host\`.

## Coverage

${renderCoverageTable(t)}

## Per-fixture status

${renderStatusTable(rows)}

## Legend

- **✅ OK** — \`loads app UI\` + \`screenshot matches golden\` Playwright
  tests pass, and the \`tools/list\` parity check matches upstream's TS
  server byte-for-byte under DOCKER mode
  (\`mcr.microsoft.com/playwright:v1.57.0-noble\`, the same image upstream
  uses for \`test:e2e:docker\`).
- **🟡 WIP / PROT** — surface working; visual baseline or interaction
  tests pending.
- **⏭ SKIP** — upstream's \`servers.spec.ts\` deliberately omits this
  example (special build-time dependency or not in the default test
  matrix).
- **⬜ NOT** — not yet implemented as an mcpkit drop-in.

## How parity is verified

Each fixture runs through \`scripts/apps-playwright-test.sh\` in two
modes:

1. **Native** (host OS) — fast \`loads app UI\` iteration. The visual
   screenshot test is Docker-pinned and is expected to fail on
   non-Linux hosts; that gap is intentional.
2. **DOCKER** — runs the upstream Playwright image with the fixture as
   the MCP server. The committed baseline is a single canonical
   Linux PNG per fixture (matching upstream's own pinning convention).
   This mode also runs a **strict \`tools/list\` parity check** that
   spins up upstream's TypeScript reference server on a side port,
   fetches \`tools/list\` from both, JSON-diffs them, and fails on any
   schema drift outside a small filter for SDK-emit differences
   (\`$schema\`, \`additionalProperties\`, \`propertyNames\`, plus an
   \`integer\` ⇄ \`number\` subtype normalization). Drift fails the gate.

## How this report is generated

\`tools/compat-reports/src/apps.ts\` fetches the body of umbrella
tracking issue \`${args.repo}#${args.umbrella}\` and renders its
status table as this Markdown file. The umbrella issue is the
hand-maintained source of truth — every fixture PR updates the row
along with the code. The script is deterministic: re-running on an
unchanged umbrella body produces a byte-identical file.

To refresh:

\`\`\`sh
just refresh-apps-compat-report
\`\`\`

The CI \`check-apps-compat-stale\` gate enforces that PRs touching
\`examples/apps/compat/**\` re-run the refresh and commit any diff.

## See also

- [Conformance Coverage](../../CONFORMANCE.md) — MCP spec compliance matrix
- [Upstream Conformance Audit](../UPSTREAM_AUDIT.md) — scenario-level grading vs upstream's conformance suite
- [Fixture Parity Audit](../FIXTURE_AUDIT.md) — public-API discipline check on conformance fixtures
- \`examples/apps/compat/README.md\` — drop-in fixture pattern + the wrapper script
`;
}

function main(): void {
  const args = parseArgs(process.argv.slice(2));
  const md = renderReport(args);
  writeFileSync(args.out, md);
  process.stdout.write(`Wrote ${args.out}\n`);
}

main();
