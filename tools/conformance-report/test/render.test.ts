import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { loadKnownGaps, loadScorecard, loadTraceability } from '../src/parse.js';
import {
  BEGIN_GENERATED,
  END_GENERATED,
  renderGeneratedBlock,
  spliceGeneratedRegion
} from '../src/render.js';

const here = fileURLToPath(new URL('./fixtures/', import.meta.url));

function loadFixtures() {
  return {
    scorecard: loadScorecard(join(here, 'scorecard.json')),
    traceability: loadTraceability(join(here, 'traceability.json')),
    knownGaps: loadKnownGaps(join(here, 'known-gaps.yaml')),
    upstreamSha: 'abc1234567890abcdef1234567890abcdef123456',
    protocolVersion: '2025-11-25'
  };
}

function trimTrailing(s: string): string {
  return s.replace(/\n+$/, '');
}

describe('renderGeneratedBlock', () => {
  it('matches the golden output for the canonical fixture', () => {
    const input = loadFixtures();
    const expected = readFileSync(join(here, 'expected.md'), 'utf-8');
    const got = renderGeneratedBlock(input);
    expect(trimTrailing(got)).toBe(trimTrailing(expected));
  });

  it('is deterministic across runs (no wall-clock, no random ordering)', () => {
    const input = loadFixtures();
    const a = renderGeneratedBlock(input);
    const b = renderGeneratedBlock(input);
    expect(a).toBe(b);
  });

  it('does not include any wall-clock date in the stamp', () => {
    const input = loadFixtures();
    const got = renderGeneratedBlock(input);
    // Wall-clock dates (e.g. ISO 2026-05-30 or year tokens) anywhere in the
    // generated block would break the staleness gate; only the upstream SHA
    // and protocol version are allowed in the stamp.
    expect(got).not.toMatch(/202\d-\d{2}-\d{2}T/);
    expect(got).not.toMatch(/generated YYYY-MM-DD/);
  });

  it('drops tier-check checks that depend on live GitHub state', () => {
    const input = loadFixtures();
    const got = renderGeneratedBlock(input);
    // None of these slices should leak into CONFORMANCE.md — they change
    // daily independent of code and would break the diff gate.
    expect(got).not.toMatch(/Labels/i);
    expect(got).not.toMatch(/Triage/i);
    expect(got).not.toMatch(/P0/);
    expect(got).not.toMatch(/Stable Release/i);
    expect(got).not.toMatch(/Policy Signals/i);
    expect(got).not.toMatch(/Spec Tracking/i);
  });

  it('shows "no open gaps" when scorecard is clean and traceability fully tested', () => {
    const input = loadFixtures();
    input.scorecard.checks.conformance.details = input.scorecard.checks.conformance.details.map((d) => ({
      ...d,
      passed: true,
      checks_failed: 0
    }));
    input.scorecard.checks.client_conformance.details = input.scorecard.checks.client_conformance.details.map((d) => ({
      ...d,
      passed: true,
      checks_failed: 0
    }));
    for (const sep of Object.values(input.traceability.seps)) {
      for (const r of sep.requirements) r.status = 'tested';
      sep.summary.untested = 0;
    }
    const got = renderGeneratedBlock(input);
    expect(got).toMatch(/No open conformance gaps/);
  });
});

describe('spliceGeneratedRegion', () => {
  it('preserves hand-edited content above the begin marker', () => {
    const preserved =
      '# CONFORMANCE\n\nThis is the overview, hand-edited by maintainers.\n\n';
    const existing = preserved + BEGIN_GENERATED + '\nOLD BLOCK\n' + END_GENERATED + '\n';
    const newBlock = BEGIN_GENERATED + '\nNEW BLOCK\n' + END_GENERATED;
    const merged = spliceGeneratedRegion(existing, newBlock);
    expect(merged.startsWith(preserved)).toBe(true);
    expect(merged).toContain('NEW BLOCK');
    expect(merged).not.toContain('OLD BLOCK');
  });

  it('appends the block when no markers exist (bootstrap case)', () => {
    const newBlock = BEGIN_GENERATED + '\nFIRST RUN\n' + END_GENERATED;
    const merged = spliceGeneratedRegion(null, newBlock);
    expect(merged).toContain('FIRST RUN');
    expect(merged.endsWith('\n')).toBe(true);
  });

  it('appends to existing content with no markers (in-flight migration)', () => {
    const existing = '# CONFORMANCE\n\nLegacy hand-written file.\n';
    const newBlock = BEGIN_GENERATED + '\nNEW\n' + END_GENERATED;
    const merged = spliceGeneratedRegion(existing, newBlock);
    expect(merged.startsWith(existing)).toBe(true);
    expect(merged).toContain('NEW');
  });

  it('ignores marker text embedded in prose (only whole-line markers anchor the splice)', () => {
    // Overview text legitimately mentions the marker convention. The first
    // textual occurrence below sits on the same line as other prose; the
    // real splice anchor is the standalone marker further down.
    const preserved =
      '# CONFORMANCE\n\nOverview mentioning the <!-- begin:generated --> marker convention inline.\n\n';
    const existing =
      preserved +
      BEGIN_GENERATED +
      '\nOLD BLOCK\n' +
      END_GENERATED +
      '\n';
    const newBlock = BEGIN_GENERATED + '\nNEW BLOCK\n' + END_GENERATED;
    const merged = spliceGeneratedRegion(existing, newBlock);
    expect(merged.startsWith(preserved)).toBe(true);
    expect(merged).toContain('marker convention inline');
    expect(merged).toContain('NEW BLOCK');
    expect(merged).not.toContain('OLD BLOCK');
  });
});
