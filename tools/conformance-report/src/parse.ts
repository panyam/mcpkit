import { readFileSync, existsSync } from 'node:fs';
import { parse as parseYaml } from 'yaml';
import type {
  KnownGaps,
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
