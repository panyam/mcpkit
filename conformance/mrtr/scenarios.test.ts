/**
 * SEP-2322 MRTR (Multi Round-Trip Requests) — ephemeral IncompleteResult
 * Conformance Scenarios
 *
 * Mirrors the upstream conformance contract from
 * modelcontextprotocol/conformance PR 188 (incomplete-result.ts), adapted
 * to the mcpkit raw-fetch test pattern used by tasks-v2/scenarios.test.ts.
 *
 * Key wire-format note: the SEP-2322 discriminator is `resultType`
 * — camelCase like every other MCP wire field. Luca confirmed camelCase
 * is the spec standard; the upstream conformance suite briefly used
 * snake_case but that's being corrected on their side.
 *
 * Server requirements: see examples/mrtr/main.go for the matching tool
 * fixture set. Each scenario tool is named per the upstream contract.
 *
 * Usage:
 *   cd conformance && npm install
 *   SERVER_URL=http://localhost:18093/mcp npx tsx --test mrtr/scenarios.test.ts
 *
 * Or via the Makefile target which builds + spawns + tears down for you:
 *   make testconf-mrtr
 */

import { describe, test, before } from 'node:test';
import { strict as assert } from 'node:assert';

const SERVER_URL = process.env.SERVER_URL || 'http://localhost:18093/mcp';

// SPEC WATCH — MRTR resultType discriminator value
//
// SEP-2322 (MRTR) and SEP-2663 (Tasks Extension) currently disagree on the
// wire value for the "needs more input" discriminator: SEP-2322's draft
// uses "input_required", SEP-2663's draft uses "incomplete". Neither PR
// documents the transition, so SDKs implementing against 2322 first would
// silently break when 2663 lands. prezaei flagged the collision on the
// SEP-2663 PR; awaiting alignment between the two SEP authors. When the
// spec converges, flipping this single const updates every assertion in
// this suite.
//
// Tracking: PR 2663 comment 4381885336 + PR 2322 comment 4381884825.
const MRTR_INCOMPLETE_RESULT_TYPE = 'incomplete';

let sessionId: string;
let nextId = 1;

// ============================================================================
// Session bootstrap (raw, SDK-free)
// ============================================================================

/**
 * Run a fresh initialize handshake and return the resulting session id.
 * We bypass the SDK because its built-in Zod schemas would strip the
 * non-standard fields this suite exercises (resultType, inputRequests,
 * requestState).
 */
async function initRawSession(capabilities: Record<string, unknown>): Promise<string> {
    const initResp = await fetch(SERVER_URL, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Accept': 'application/json' },
        body: JSON.stringify({
            jsonrpc: '2.0', id: 'init-raw', method: 'initialize',
            params: {
                protocolVersion: '2025-11-25',
                clientInfo: { name: 'mrtr-conformance', version: '1.0' },
                capabilities,
            },
        }),
    });
    const sid = initResp.headers.get('mcp-session-id') || '';
    if (!sid) throw new Error('initialize response missing Mcp-Session-Id');

    await fetch(SERVER_URL, {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'Accept': 'application/json',
            'Mcp-Session-Id': sid,
        },
        body: JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized' }),
    });
    return sid;
}

before(async () => {
    // Declare elicitation/sampling/roots so the server's MRTR inputRequests
    // are well-formed against capabilities — even though the conformance
    // suite mocks the responses (we don't actually answer the embedded
    // sampling/createMessage etc., we just shape inputResponses ourselves).
    sessionId = await initRawSession({
        elicitation: {},
        sampling: {},
        roots: {},
    });
});

// ============================================================================
// Raw JSON-RPC plumbing
// ============================================================================

async function rawRequest(method: string, params: any): Promise<any> {
    const id = nextId++;
    const resp = await fetch(SERVER_URL, {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'Accept': 'text/event-stream, application/json',
            'Mcp-Session-Id': sessionId,
        },
        body: JSON.stringify({ jsonrpc: '2.0', id, method, params }),
    });
    const ct = resp.headers.get('content-type') || '';
    let body: any;
    if (ct.includes('text/event-stream')) {
        const text = await resp.text();
        for (const line of text.split('\n')) {
            const trimmed = line.trim();
            if (trimmed.startsWith('data:')) {
                const payload = trimmed.slice(5).trimStart();
                if (payload.startsWith('{')) {
                    const parsed = JSON.parse(payload);
                    if (parsed.id === id) { body = parsed; break; }
                }
            }
        }
    } else {
        body = await resp.json();
    }
    if (!body) throw new Error(`No JSON-RPC response for ${method}`);
    if (body.error) {
        const err: any = new Error(body.error.message);
        err.code = body.error.code;
        err.data = body.error.data;
        throw err;
    }
    return body.result;
}

// ============================================================================
// MRTR-specific helpers
// ============================================================================

function isIncompleteResult(result: any): boolean {
    if (!result) return false;
    if (result.resultType === MRTR_INCOMPLETE_RESULT_TYPE) return true;
    return 'inputRequests' in result || 'requestState' in result;
}

function isCompleteResult(result: any): boolean {
    if (!result) return false;
    if (result.resultType === 'complete') return true;
    if (!('resultType' in result)) return true;
    return !isIncompleteResult(result);
}

/** Build an ElicitResult-shaped mock response payload. */
function mockElicitResponse(content: Record<string, unknown>): Record<string, unknown> {
    return { action: 'accept', content };
}

/** Build a CreateMessageResult-shaped mock response payload. */
function mockSamplingResponse(text: string): Record<string, unknown> {
    return {
        role: 'assistant',
        content: { type: 'text', text },
        model: 'test-model',
        stopReason: 'endTurn',
    };
}

/** Build a ListRootsResult-shaped mock response payload. */
function mockListRootsResponse(): Record<string, unknown> {
    return { roots: [{ uri: 'file:///test/root', name: 'Test Root' }] };
}

// ============================================================================
// Scenarios — mirror upstream PR 188 incomplete-result.ts A1..A7
// ============================================================================

describe('SEP-2322 MRTR ephemeral IncompleteResult flow', () => {

    // -------- A1: basic elicitation --------
    test('mrtr-01: tools/call returns IncompleteResult on round 1, complete on round 2', async () => {
        const r1 = await rawRequest('tools/call', {
            name: 'test_tool_with_elicitation',
            arguments: {},
        });
        assert.ok(isIncompleteResult(r1), `round 1 must be IncompleteResult; got ${JSON.stringify(r1)}`);
        assert.equal(r1.resultType, MRTR_INCOMPLETE_RESULT_TYPE,
            `resultType discriminator must be camelCase "${MRTR_INCOMPLETE_RESULT_TYPE}"`);
        assert.ok(r1.inputRequests, 'IncompleteResult must carry inputRequests');
        assert.ok(r1.inputRequests.user_name, 'inputRequests must include "user_name" key');
        assert.equal(r1.inputRequests.user_name.method, 'elicitation/create');

        const r2 = await rawRequest('tools/call', {
            name: 'test_tool_with_elicitation',
            arguments: {},
            inputResponses: { user_name: mockElicitResponse({ name: 'Alice' }) },
            ...(r1.requestState !== undefined ? { requestState: r1.requestState } : {}),
        });
        assert.ok(isCompleteResult(r2), `round 2 must be complete; got ${JSON.stringify(r2)}`);
        assert.ok(Array.isArray(r2.content) && r2.content.length > 0, 'round 2 must have content[]');
        assert.match(r2.content[0].text, /Alice/, 'response text should reference the answered name');
    });

    // -------- A2: basic sampling --------
    test('mrtr-02: sampling/createMessage round-trip', async () => {
        const r1 = await rawRequest('tools/call', {
            name: 'test_incomplete_result_sampling',
            arguments: {},
        });
        assert.ok(isIncompleteResult(r1), 'round 1 must be IncompleteResult');
        const key = Object.keys(r1.inputRequests)[0];
        assert.equal(r1.inputRequests[key].method, 'sampling/createMessage');

        const r2 = await rawRequest('tools/call', {
            name: 'test_incomplete_result_sampling',
            arguments: {},
            inputResponses: { [key]: mockSamplingResponse('Paris') },
            ...(r1.requestState !== undefined ? { requestState: r1.requestState } : {}),
        });
        assert.ok(isCompleteResult(r2), 'round 2 must be complete');
    });

    // -------- A3: basic roots/list --------
    test('mrtr-03: roots/list round-trip', async () => {
        const r1 = await rawRequest('tools/call', {
            name: 'test_incomplete_result_list_roots',
            arguments: {},
        });
        assert.ok(isIncompleteResult(r1));
        const key = Object.keys(r1.inputRequests)[0];
        assert.equal(r1.inputRequests[key].method, 'roots/list');

        const r2 = await rawRequest('tools/call', {
            name: 'test_incomplete_result_list_roots',
            arguments: {},
            inputResponses: { [key]: mockListRootsResponse() },
            ...(r1.requestState !== undefined ? { requestState: r1.requestState } : {}),
        });
        assert.ok(isCompleteResult(r2));
    });

    // -------- A4: requestState round-trip --------
    test('mrtr-04: requestState is non-empty on round 1 and validated on round 2', async () => {
        const r1 = await rawRequest('tools/call', {
            name: 'test_incomplete_result_request_state',
            arguments: {},
        });
        assert.ok(isIncompleteResult(r1));
        assert.equal(typeof r1.requestState, 'string', 'requestState must be a string');
        assert.ok(r1.requestState.length > 0, 'requestState must be non-empty when the server explicitly emits one');

        const key = Object.keys(r1.inputRequests)[0];
        const r2 = await rawRequest('tools/call', {
            name: 'test_incomplete_result_request_state',
            arguments: {},
            inputResponses: { [key]: mockElicitResponse({ ok: true }) },
            requestState: r1.requestState,
        });
        assert.ok(isCompleteResult(r2));
        const text = (r2.content?.find((c: any) => c.type === 'text')?.text) ?? '';
        assert.match(text, /state-ok/, 'response text should include "state-ok" to confirm requestState validated');
    });

    // -------- A5: multiple input requests in one round --------
    test('mrtr-05: a single IncompleteResult can carry multiple inputRequests of different methods', async () => {
        const r1 = await rawRequest('tools/call', {
            name: 'test_incomplete_result_multiple_inputs',
            arguments: {},
        });
        assert.ok(isIncompleteResult(r1));
        const keys = Object.keys(r1.inputRequests);
        assert.ok(keys.length >= 3, `expected at least 3 inputRequests; got ${keys.length}`);
        const methods = new Set(keys.map(k => r1.inputRequests[k].method));
        assert.ok(methods.has('elicitation/create'));
        assert.ok(methods.has('sampling/createMessage'));
        assert.ok(methods.has('roots/list'));

        const inputResponses: Record<string, unknown> = {};
        for (const [key, req] of Object.entries(r1.inputRequests) as Array<[string, any]>) {
            if (req.method === 'elicitation/create') inputResponses[key] = mockElicitResponse({ name: 'Alice' });
            else if (req.method === 'sampling/createMessage') inputResponses[key] = mockSamplingResponse('hi');
            else if (req.method === 'roots/list') inputResponses[key] = mockListRootsResponse();
        }
        const r2 = await rawRequest('tools/call', {
            name: 'test_incomplete_result_multiple_inputs',
            arguments: {},
            inputResponses,
            ...(r1.requestState !== undefined ? { requestState: r1.requestState } : {}),
        });
        assert.ok(isCompleteResult(r2));
    });

    // -------- A6: multi-round (incomplete → incomplete → complete) --------
    test('mrtr-06: multi-round flow accumulates answers across rounds via requestState', async () => {
        // Round 1
        const r1 = await rawRequest('tools/call', {
            name: 'test_incomplete_result_multi_round',
            arguments: {},
        });
        assert.ok(isIncompleteResult(r1));
        assert.ok(r1.requestState, 'round 1 must mint requestState for multi-round flow');
        const k1 = Object.keys(r1.inputRequests)[0];

        // Round 2 — answer step1, expect another IncompleteResult (step2)
        const r2 = await rawRequest('tools/call', {
            name: 'test_incomplete_result_multi_round',
            arguments: {},
            inputResponses: { [k1]: mockElicitResponse({ name: 'Alice' }) },
            requestState: r1.requestState,
        });
        assert.ok(isIncompleteResult(r2), 'round 2 must still be IncompleteResult (asks for step2)');
        assert.ok(r2.requestState, 'round 2 must mint a fresh requestState');
        assert.notEqual(r2.requestState, r1.requestState, 'round 2 requestState must differ from round 1');
        const k2 = Object.keys(r2.inputRequests)[0];

        // Round 3 — answer step2 ONLY (no step1 in this round); server must
        // forward step1 via requestState and surface BOTH answers to handler.
        const r3 = await rawRequest('tools/call', {
            name: 'test_incomplete_result_multi_round',
            arguments: {},
            inputResponses: { [k2]: mockElicitResponse({ color: 'blue' }) },
            requestState: r2.requestState,
        });
        assert.ok(isCompleteResult(r3), 'round 3 must be complete');
        const text = r3.content?.[0]?.text ?? '';
        assert.match(text, /Alice/, 'final text must reference round 1 answer (Alice)');
        assert.match(text, /blue/, 'final text must reference round 2 answer (blue)');
    });

    // -------- A7: missing inputResponse handling --------
    test('mrtr-07: server re-requests via IncompleteResult when client sends wrong inputResponses key', async () => {
        const r1 = await rawRequest('tools/call', {
            name: 'test_incomplete_result_elicitation',
            arguments: {},
            inputResponses: { wrong_key: mockElicitResponse({ data: 'wrong' }) },
        });
        // SEP-2322 prefers re-request over error. Either is technically
        // tolerable per the upstream test (warning vs failure), but our
        // implementation re-requests, so assert the strict behavior.
        assert.ok(isIncompleteResult(r1),
            `expected IncompleteResult re-request when inputResponses key is wrong; got ${JSON.stringify(r1)}`);
    });

    // -------- B1 (skipped): MRTR → Tasks composition --------
    test.skip('mrtr-08: MRTR loop gathers input then final round returns CreateTaskResult (deferred)', async () => {
        // Tracking placeholder — matches server/mrtr_test.go:TestMRTR_TaskComposition_Skipped.
        // SEP-2663 commit 451f5e1 made this flow normative.
        //
        // Two blockers; both must be resolved before this test can be enabled:
        //
        // 1. Implementation gap (mcpkit issue 347) — the v2 task middleware
        //    creates the task BEFORE the handler runs, so it never observes
        //    the handler's IsIncomplete signal. Re-enabling this test
        //    requires inverting the middleware so round 1 runs synchronously
        //    and the task is only spun up on a handler-signalled async path.
        //
        // 2. Spec watch (prezaei comment on PR 2663) — SEP-2322 and SEP-2663
        //    currently disagree on whether the MRTR discriminator value is
        //    "input_required" or "incomplete". This scenario uses
        //    MRTR_INCOMPLETE_RESULT_TYPE so the eventual spec resolution is
        //    a single-line flip.
        const r1 = await rawRequest('tools/call', {
            name: 'test_tool_with_task',
            arguments: {},
        });
        assert.ok(isIncompleteResult(r1), 'round 1 should be IncompleteResult before the final task hand-off');
        const key = Object.keys(r1.inputRequests)[0];
        const r2 = await rawRequest('tools/call', {
            name: 'test_tool_with_task',
            arguments: {},
            inputResponses: { [key]: mockElicitResponse({ value: 'x' }) },
            requestState: r1.requestState,
        });
        assert.equal(r2.resultType, 'task',
            'final round should promote sync result to CreateTaskResult');
        assert.ok(r2.taskId, 'CreateTaskResult must include taskId (SEP-2663 flat shape)');
    });
});
