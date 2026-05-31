// Mirrors the subset of upstream tier-check output we consume. Kept narrow on
// purpose — only the fields the renderer reads. If upstream renames or removes
// one of these we get a TS error at parse time, which is the signal to update
// fixtures and the recorded golden output.
//
// Upstream source of truth: modelcontextprotocol/conformance, src/tier-check/types.ts.

export type CheckStatus = 'pass' | 'fail' | 'partial' | 'skipped';

export interface ConformanceScenarioResult {
  scenario: string;
  passed: boolean;
  checks_passed: number;
  checks_failed: number;
  specVersions?: string[];
}

export interface ConformanceResult {
  status: CheckStatus;
  pass_rate: number;
  passed: number;
  failed: number;
  total: number;
  details: ConformanceScenarioResult[];
}

export interface TierScorecard {
  repo: string;
  branch: string | null;
  timestamp: string;
  version: string | null;
  checks: {
    conformance: ConformanceResult;
    client_conformance: ConformanceResult;
    // Other tier-check checks (labels, triage, p0_resolution, stable_release,
    // policy_signals, spec_tracking) are intentionally NOT typed here — the
    // renderer drops them so they cannot influence CONFORMANCE.md, which
    // would break the CI staleness gate (they depend on live GH state).
  };
  implied_tier: {
    tier: 1 | 2 | 3;
    tier1_blockers: string[];
    tier2_met: boolean;
    note: string;
  };
}

// --- Upstream traceability manifest (src/seps/traceability.json) ------------

export interface TraceabilityRequirement {
  check: string;
  status: 'tested' | 'untested';
  text?: string;
  url?: string;
  issue?: string;
}

export interface TraceabilityExclusion {
  text: string;
  reason: string;
  issue?: string;
}

export interface SepTraceability {
  yaml: string | null;
  specUrl: string | null;
  requirements: TraceabilityRequirement[];
  excluded: TraceabilityExclusion[];
  unkeyed: Array<{ text: string }>;
  untracked: string[];
  summary: {
    tested: number;
    untested: number;
    excluded: number;
    untracked: number;
    unkeyed: number;
  };
}

export interface TraceabilityManifest {
  schemaVersion: number;
  docs: string;
  source: string | null;
  seps: Record<string, SepTraceability>;
}

// --- Local hand-annotated gap overlay ---------------------------------------

export interface KnownGapEntry {
  issue?: string;
  note?: string;
}

export interface KnownGaps {
  // Keys are scenario IDs (e.g. "tools/dynamic") or check IDs
  // (e.g. "sep-2322-input-required-on-allowed-method"). First match wins.
  scenarios?: Record<string, KnownGapEntry>;
  checks?: Record<string, KnownGapEntry>;
}

// --- Render input -----------------------------------------------------------

export interface RenderInput {
  scorecard: TierScorecard;
  traceability: TraceabilityManifest;
  knownGaps: KnownGaps;
  // Provenance — used for the stamp comment at the top of the generated block.
  upstreamSha: string;
  protocolVersion: string;
}
