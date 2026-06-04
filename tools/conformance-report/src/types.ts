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

// --- mcpkit-local conformance suites manifest -------------------------------
//
// Source: conformance/local-suites.yaml. Surfaces SEP-specific suites that
// run alongside upstream tier-check. The renderer emits a table from this
// manifest into CONFORMANCE.md between the upstream Conformance Summary and
// the SEP Coverage section.

export type LocalSuiteStatus = 'PASS' | 'FAIL' | 'INFO' | 'SKIP';

// Describes where a suite's scenarios live. mcpkit points at separate
// worktrees of panyam/mcpconformance because different SEPs ride on
// different branches while their upstream PRs are still draft; one suite
// (testconf-stateless) points at the real upstream
// modelcontextprotocol/conformance@main. Rendered into CONFORMANCE.md as
// the "Source" column plus a setup subsection so readers know where to
// clone from.
export interface LocalSuiteSource {
  // GitHub `owner/repo` slug. Used to build the branch link.
  repo: string;
  // Branch the suite expects the worktree to be checked out at.
  branch: string;
  // The MCPCONFORMANCE_*_PATH env var the testconf-* target reads to
  // locate the worktree.
  pathVar: string;
  // Default value if the env var is not set. Relative to the repo root.
  defaultPath: string;
}

export interface LocalSuite {
  // Make target name, e.g. "testconf-skills".
  suite: string;
  // Short human-readable description of what the suite covers, used as the
  // "Covers" column (e.g. "SEP-2640 Skills").
  sep: string;
  // testall stage label, e.g. "8h". "-" for suites that exist as standalone
  // targets but are not wired into testall.
  stage: string;
  // Current declared status. Mismatches with the Makefile wiring are caught
  // by scripts/check_local_suites.py in CI.
  status: LocalSuiteStatus;
  // Where the scenarios live. Optional only during the initial bootstrap of
  // a suite; existing suites must declare this so readers know what to
  // clone.
  source?: LocalSuiteSource;
  // Optional one-line footnote attached as a numeric reference.
  note?: string;
  // Plain-text tracking reference (issue/PR number); avoids GitHub backlink
  // firing. Rendered alongside the status row.
  tracking?: string;
}

export interface LocalSuitesManifest {
  suites: LocalSuite[];
}

// --- Render input -----------------------------------------------------------

export interface RenderInput {
  scorecard: TierScorecard;
  traceability: TraceabilityManifest;
  knownGaps: KnownGaps;
  // Hand-maintained manifest of mcpkit-local conformance suites.
  localSuites: LocalSuitesManifest;
  // Provenance — used for the stamp comment at the top of the generated block.
  upstreamSha: string;
  protocolVersion: string;
}
