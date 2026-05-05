/**
 * SEP-2322 MRTR conformance — mcpkit-local sentinel.
 *
 * The MRTR scenario suite was migrated upstream to the conformance fork
 * (panyam/mcpconformance, branch feat/tasks-mrtr-extension; eventually
 * upstreamed to modelcontextprotocol/conformance). Run it via:
 *
 *     make testconf-mrtr
 *
 * which delegates to vitest in the fork.
 *
 * This file is a placeholder. The folder is kept around for any
 * future mcpkit-stricter MRTR scenarios — checks that go beyond what
 * the spec mandates because mcpkit deliberately picks the louder/
 * safer option where the spec is silent. Add such tests here directly
 * using vitest; run via:
 *
 *     cd conformance && npm install && npx vitest run mrtr/
 *
 * Today there are no such mcpkit-stricter tests; the sentinel just
 * keeps the folder discoverable.
 */

import { describe, it, expect } from 'vitest';

describe('mcpkit-mrtr (sentinel)', () => {
    it('is a placeholder for future mcpkit-stricter scenarios', () => {
        expect(true).toBe(true);
    });
});
