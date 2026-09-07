/**
 * SEP-2663 Tasks conformance — mcpkit-local sentinel.
 *
 * The full SEP-2663 / SEP-2322 / SEP-2575 / SEP-2243 scenario suite now
 * lives in modelcontextprotocol/conformance on main. It travelled there
 * through the panyam/mcpconformance fork (branch
 * feat/tasks-mrtr-extension), but that hop is history:
 * MCPCONFORMANCE_TASKS_V2_PATH defaults to ../conf-upstream-main, a direct
 * clone of upstream. Run it via:
 *
 *     make testconf-tasks-v2
 *
 * which drives examples/tasks-v2 through the upstream conformance CLI one
 * scenario at a time and gates on zero FAILURE rows. It does NOT delegate
 * to vitest any more; the old shape ran an upstream vitest file that
 * spawned its own fixture and graded the reference server rather than
 * mcpkit. See conformance/scripts/conf-tasks-v2.sh.
 *
 * This file is a placeholder. The folder is kept around for any
 * future mcpkit-stricter conformance scenarios — checks that go
 * beyond what the spec mandates because mcpkit deliberately picks
 * the louder/safer option where the spec is silent. Add such tests
 * here directly using vitest; run via:
 *
 *     cd conformance && npm install && npx vitest run tasks-v2/
 *
 * Today there are no such mcpkit-stricter tests; the sentinel just
 * keeps the folder discoverable.
 */

import { describe, it, expect } from 'vitest';

describe('mcpkit-tasks-v2 (sentinel)', () => {
    it('is a placeholder for future mcpkit-stricter scenarios', () => {
        expect(true).toBe(true);
    });
});
