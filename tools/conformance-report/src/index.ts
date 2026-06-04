#!/usr/bin/env node
// CLI entry. Parses flags, loads inputs via parse.ts, splices the renderer
// output into the existing CONFORMANCE.md (preserving the hand-edited region
// above <!-- begin:generated -->), writes the file.
//
// Invocation (see scripts/refresh-conformance.sh):
//   conformance-report \
//     --scorecard <tier-check.json> \
//     --traceability <upstream/src/seps/traceability.json> \
//     --known-gaps <conformance/known-gaps.yaml> \
//     --out CONFORMANCE.md \
//     --upstream-sha <sha> \
//     --protocol <ver>
import { readFileSync, writeFileSync, existsSync } from 'node:fs';
import {
  loadScorecard,
  loadTraceability,
  loadKnownGaps,
  loadLocalSuites
} from './parse.js';
import { renderGeneratedBlock, spliceGeneratedRegion } from './render.js';

interface CliArgs {
  scorecard: string;
  traceability: string;
  knownGaps: string;
  localSuites: string;
  out: string;
  upstreamSha: string;
  protocol: string;
}

function parseArgs(argv: string[]): CliArgs {
  const out: Partial<CliArgs> = {};
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    const consume = () => {
      const v = argv[++i];
      if (!v) throw new Error(`flag ${arg} requires a value`);
      return v;
    };
    switch (arg) {
      case '--scorecard':
        out.scorecard = consume();
        break;
      case '--traceability':
        out.traceability = consume();
        break;
      case '--known-gaps':
        out.knownGaps = consume();
        break;
      case '--local-suites':
        out.localSuites = consume();
        break;
      case '--out':
        out.out = consume();
        break;
      case '--upstream-sha':
        out.upstreamSha = consume();
        break;
      case '--protocol':
        out.protocol = consume();
        break;
      case '--help':
      case '-h':
        usage();
        process.exit(0);
      default:
        throw new Error(`unknown flag: ${arg}`);
    }
  }
  for (const k of ['scorecard', 'traceability', 'knownGaps', 'localSuites', 'out', 'upstreamSha', 'protocol'] as const) {
    if (!out[k]) throw new Error(`missing required flag: --${k.replace(/([A-Z])/g, '-$1').toLowerCase()}`);
  }
  return out as CliArgs;
}

function usage(): void {
  process.stderr.write(
    `conformance-report --scorecard <path> --traceability <path> --known-gaps <path> --local-suites <path> --out <path> --upstream-sha <sha> --protocol <ver>\n`
  );
}

function main(): void {
  const args = parseArgs(process.argv.slice(2));
  const scorecard = loadScorecard(args.scorecard);
  const traceability = loadTraceability(args.traceability);
  const knownGaps = loadKnownGaps(args.knownGaps);
  const localSuites = loadLocalSuites(args.localSuites);
  const block = renderGeneratedBlock({
    scorecard,
    traceability,
    knownGaps,
    localSuites,
    upstreamSha: args.upstreamSha,
    protocolVersion: args.protocol
  });
  const existing = existsSync(args.out) ? readFileSync(args.out, 'utf-8') : null;
  const merged = spliceGeneratedRegion(existing, block);
  writeFileSync(args.out, merged);
}

try {
  main();
} catch (err) {
  process.stderr.write(`conformance-report: ${(err as Error).message}\n`);
  process.exit(1);
}
