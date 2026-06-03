import { describe, it, expect } from 'vitest';
import type { StatusRow } from '../src/lib/umbrella.js';
import {
  renderCoverageTable,
  renderStatusTable,
  statusBadge,
  tally,
} from '../src/lib/render.js';

const SAMPLE: StatusRow[] = [
  { num: '1', name: 'fixture-a', status: 'OK',   notes: 'first row, PR 100' },
  { num: '2', name: 'fixture-b', status: 'OK',   notes: 'second row' },
  { num: '3', name: 'fixture-c', status: 'WIP',  notes: 'in progress' },
  { num: '4', name: 'fixture-d', status: 'SKIP', notes: 'upstream skipped' },
  { num: '5', name: 'fixture-e', status: 'NOT',  notes: 'not yet built' },
];

describe('statusBadge', () => {
  it('maps known statuses to their emoji form', () => {
    expect(statusBadge('OK')).toBe('✅ OK');
    expect(statusBadge('WIP')).toBe('🟡 WIP');
    expect(statusBadge('PROT')).toBe('🟡 PROT');
    expect(statusBadge('SKIP')).toBe('⏭ SKIP');
    expect(statusBadge('NOT')).toBe('⬜ NOT');
  });

  it('falls back for unknown statuses so typos surface visibly', () => {
    expect(statusBadge('UNKOWN')).toBe('⬜ UNKOWN');
  });
});

describe('tally', () => {
  it('counts each status and computes implemented + pass rate', () => {
    const t = tally(SAMPLE);
    expect(t.total).toBe(5);
    expect(t.ok).toBe(2);
    expect(t.wip).toBe(1);
    expect(t.skip).toBe(1);
    expect(t.not).toBe(1);
    expect(t.implemented).toBe(3); // OK + WIP
    expect(t.passRate).toBe('67%');
  });

  it('returns an em-dash pass rate when nothing is implemented', () => {
    const t = tally([
      { num: '1', name: 'x', status: 'SKIP', notes: '' },
      { num: '2', name: 'y', status: 'NOT',  notes: '' },
    ]);
    expect(t.passRate).toBe('—');
  });

  it('100% pass rate renders as 100% not 100.0%', () => {
    const t = tally([{ num: '1', name: 'x', status: 'OK', notes: '' }]);
    expect(t.passRate).toBe('100%');
  });
});

describe('renderStatusTable', () => {
  it('includes the markdown header + separator + one row per input', () => {
    const out = renderStatusTable(SAMPLE);
    const lines = out.split('\n');
    expect(lines[0]).toBe('| # | Fixture | Status | Detail |');
    expect(lines[1]).toBe('|---|---|---|---|');
    expect(lines).toHaveLength(2 + SAMPLE.length);
  });

  it('passes notes through verbatim so embedded markdown renders on the page', () => {
    const out = renderStatusTable(SAMPLE);
    expect(out).toContain('first row, PR 100');
  });
});

describe('renderCoverageTable', () => {
  it('emits the metric labels the legend documents', () => {
    const out = renderCoverageTable(tally(SAMPLE));
    expect(out).toContain('Total upstream examples');
    expect(out).toContain('Drift-strict parity (OK)');
    expect(out).toContain('Pass rate over implemented');
  });
});
