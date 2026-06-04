import { readFileSync, existsSync } from 'node:fs';
import { parse as parseYaml } from 'yaml';
import type {
  KnownGaps,
  LocalSuite,
  LocalSuiteStatus,
  LocalSuitesManifest,
  TierScorecard,
  TraceabilityManifest
} from './types.js';

// loadScorecard reads a tier-check `--output json` artifact and returns the
// typed scorecard. Throws on missing or malformed input — callers are
// expected to surface the failure to the user, not silently render an
// empty report.
export function loadScorecard(path: string): TierScorecard {
  const raw = readFileSync(path, 'utf-8');
  return JSON.parse(raw) as TierScorecard;
}

// loadTraceability reads `src/seps/traceability.json` from an upstream
// conformance clone. Throws on missing or malformed input.
export function loadTraceability(path: string): TraceabilityManifest {
  const raw = readFileSync(path, 'utf-8');
  return JSON.parse(raw) as TraceabilityManifest;
}

// loadKnownGaps reads the local hand-annotated gap overlay. Returns an empty
// object (no annotations) if the file is missing — the overlay is optional,
// not required for rendering.
export function loadKnownGaps(path: string): KnownGaps {
  if (!existsSync(path)) {
    return {};
  }
  const raw = readFileSync(path, 'utf-8');
  const parsed = parseYaml(raw);
  if (!parsed || typeof parsed !== 'object') {
    return {};
  }
  return parsed as KnownGaps;
}

// loadLocalSuites reads the mcpkit-local conformance suites manifest.
// Returns an empty `{ suites: [] }` if the file is missing so the renderer
// can produce a no-op section rather than crashing — useful in fixtures and
// during the transition before the YAML lands in main. Throws on malformed
// status values so a typo (e.g. "Pass" instead of "PASS") fails loudly.
export function loadLocalSuites(path: string): LocalSuitesManifest {
  if (!existsSync(path)) {
    return { suites: [] };
  }
  const raw = readFileSync(path, 'utf-8');
  const parsed = parseYaml(raw);
  if (!parsed || typeof parsed !== 'object') {
    return { suites: [] };
  }
  const suites = ((parsed as { suites?: unknown }).suites as LocalSuite[]) ?? [];
  if (!Array.isArray(suites)) {
    throw new Error(`local-suites.yaml: \`suites\` must be a list`);
  }
  const valid: LocalSuiteStatus[] = ['PASS', 'FAIL', 'INFO', 'SKIP'];
  for (const s of suites) {
    if (!s.suite || !s.sep || !s.stage || !s.status) {
      throw new Error(
        `local-suites.yaml: entry missing required field (suite, sep, stage, status): ${JSON.stringify(s)}`
      );
    }
    if (!valid.includes(s.status)) {
      throw new Error(
        `local-suites.yaml: invalid status ${JSON.stringify(s.status)} for suite ${s.suite}. Expected one of ${valid.join(', ')}`
      );
    }
  }
  return { suites };
}
