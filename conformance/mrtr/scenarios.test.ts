/**
 * SEP-2322 MRTR conformance — mcpkit-local sentinel.
 *
 * The MRTR scenario suite now lives in modelcontextprotocol/conformance on
 * main. It travelled there through the panyam/mcpconformance fork (branch
 * feat/tasks-mrtr-extension), but that hop is history:
 * MCPCONFORMANCE_MRTR_PATH defaults to ../conf-upstream-main, a direct
 * clone of upstream. Run it via:
 *
 *     make testconf-mrtr
 *
 * which drives cmd/testserver through the upstream input-required-result-*
 * scenarios via the conformance CLI, then runs upstream's negative-mrtr
 * suite as a harness self-check. The fixture is cmd/testserver, not
 * examples/mrtr: only cmd/testserver/conformance_input_required.go
 * registers the test_input_required_result_* tools the scenarios call.
 * See conformance/scripts/conf-mrtr.sh.
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
