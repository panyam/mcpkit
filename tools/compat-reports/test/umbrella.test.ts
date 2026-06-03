import { describe, it, expect } from 'vitest';
import { parseStatusTable } from '../src/lib/umbrella.js';

const FIXTURE_BODY = `## Why

Some preamble we should skip.

## Status

Legend: NOT = not-implemented, OK = all-pass, SKIP = skipped upstream

| # | Example | Status | Notes |
|---|---|---|---|
| 1 | basic-server-vanillajs | OK | 2/2 pass. PR 534 |
| 2 | basic-server-preact | OK | 2/2 pass. PR 540 |
| 3 | lazy-auth-server | SKIP | Not in upstream's servers.spec.ts |
| 4 | pdf-server | NOT | Lots of additional pdf-* spec files |

## Out of scope

- Some stuff we should NOT pick up.

| 99 | should-be-ignored | OK | this row is after the Status section |
`;

describe('parseStatusTable', () => {
  it('extracts only rows inside the ## Status section', () => {
    const rows = parseStatusTable(FIXTURE_BODY);
    expect(rows).toHaveLength(4);
    expect(rows.map((r) => r.name)).toEqual([
      'basic-server-vanillajs',
      'basic-server-preact',
      'lazy-auth-server',
      'pdf-server',
    ]);
  });

  it('preserves notes verbatim including PR refs', () => {
    const rows = parseStatusTable(FIXTURE_BODY);
    expect(rows[0].notes).toBe('2/2 pass. PR 534');
    expect(rows[3].notes).toBe('Lots of additional pdf-* spec files');
  });

  it('captures the status column for each row', () => {
    const rows = parseStatusTable(FIXTURE_BODY);
    expect(rows.map((r) => r.status)).toEqual(['OK', 'OK', 'SKIP', 'NOT']);
  });

  it('skips header and separator rows', () => {
    const rows = parseStatusTable(FIXTURE_BODY);
    // Header `| # | Example | ...` doesn't start with `| <digit>` so it
    // would be filtered by the prefix guard. Same for the separator.
    expect(rows.find((r) => r.name === 'Example')).toBeUndefined();
  });

  it('returns an empty array when there is no Status section', () => {
    expect(parseStatusTable('# Some doc\n\nNo status section here.\n')).toEqual([]);
  });

  it('stops scanning at the next top-level heading', () => {
    // The "99 | should-be-ignored" row sits past the closing `## Out of scope`
    // heading and must not appear in the output.
    const rows = parseStatusTable(FIXTURE_BODY);
    expect(rows.find((r) => r.name === 'should-be-ignored')).toBeUndefined();
  });
});
