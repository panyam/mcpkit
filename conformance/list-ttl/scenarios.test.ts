/**
 * SEP-2549 — TTL for List Results — Conformance Scenarios
 *
 * Tests an MCP server that emits the optional `ttl` (in seconds) cache-
 * freshness hint on every paginated list response (tools/list,
 * prompts/list, resources/list, resources/templates/list).
 *
 * Three-state contract per the spec:
 *   - absent  (`ttl` field omitted)         — no server guidance, fall
 *                                             back to list_changed
 *   - 0       (`"ttl": 0` explicit)         — do not cache, always
 *                                             re-fetch
 *   - >0      (`"ttl": N`)                  — fresh for N seconds
 *
 * Conformance fires three independent server processes — one per TTL
 * state — so all three wire shapes round-trip through the actual server
 * dispatcher (not just the type system). The Makefile target
 * `testconf-list-ttl` spawns + tears them down.
 *
 * Server URLs come from env (set by the Makefile):
 *   SERVER_URL_POSITIVE — server with WithListTTL(60)
 *   SERVER_URL_ZERO     — server with WithListTTL(0)
 *   SERVER_URL_UNSET    — server without WithListTTL
 *
 * Usage (manual — usually invoked via `make testconf-list-ttl`):
 *   SERVER_URL_POSITIVE=http://localhost:18094/mcp \
 *   SERVER_URL_ZERO=http://localhost:18095/mcp \
 *   SERVER_URL_UNSET=http://localhost:18096/mcp \
 *   npx tsx --test list-ttl/scenarios.test.ts
 */

import { describe, test } from 'node:test';
import { strict as assert } from 'node:assert';

const URL_POSITIVE = process.env.SERVER_URL_POSITIVE || 'http://localhost:18094/mcp';
const URL_ZERO     = process.env.SERVER_URL_ZERO     || 'http://localhost:18095/mcp';
const URL_UNSET    = process.env.SERVER_URL_UNSET    || 'http://localhost:18096/mcp';

const LIST_METHODS = [
    'tools/list',
    'prompts/list',
    'resources/list',
    'resources/templates/list',
];

// ============================================================================
// Raw JSON-RPC plumbing — bypasses SDK schema validation that would strip
// the `ttl` field from list responses.
// ============================================================================

let nextId = 1;

async function initRawSession(serverUrl: string): Promise<string> {
    const resp = await fetch(serverUrl, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Accept': 'application/json' },
        body: JSON.stringify({
            jsonrpc: '2.0', id: 'init', method: 'initialize',
            params: {
                protocolVersion: '2025-11-25',
                clientInfo: { name: 'list-ttl-conformance', version: '1.0' },
                capabilities: {},
            },
        }),
    });
    const sid = resp.headers.get('mcp-session-id') || '';
    if (!sid) throw new Error(`initialize at ${serverUrl} missing Mcp-Session-Id`);
    await fetch(serverUrl, {
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

async function rawCall(serverUrl: string, sid: string, method: string, params: any = null): Promise<any> {
    const id = nextId++;
    const resp = await fetch(serverUrl, {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'Accept': 'text/event-stream, application/json',
            'Mcp-Session-Id': sid,
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
    if (!body) throw new Error(`No response for ${method}`);
    if (body.error) {
        const err: any = new Error(body.error.message);
        err.code = body.error.code;
        throw err;
    }
    return body.result;
}

// ============================================================================
// Scenarios
// ============================================================================

describe('SEP-2549 TTL for List Results', () => {

    // Positive TTL — value surfaces unchanged on every list endpoint.
    test('list-ttl-01: positive TTL surfaces on all four list endpoints', async () => {
        const sid = await initRawSession(URL_POSITIVE);
        for (const method of LIST_METHODS) {
            const result = await rawCall(URL_POSITIVE, sid, method);
            assert.equal(typeof result.ttl, 'number',
                `${method}: ttl must be a number; got ${typeof result.ttl}`);
            assert.equal(result.ttl, 60,
                `${method}: ttl = ${result.ttl}, want 60 (server WithListTTL(60))`);
        }
    });

    // Explicit zero — the spec's "do not cache" sentinel. MUST NOT be
    // omitted/conflated with the absent case. This is the test that
    // catches a naive `int` field with `omitempty` (would drop &0).
    test('list-ttl-02: explicit zero TTL is preserved (not omitted)', async () => {
        const sid = await initRawSession(URL_ZERO);
        for (const method of LIST_METHODS) {
            const result = await rawCall(URL_ZERO, sid, method);
            assert.ok('ttl' in result,
                `${method}: ttl field MUST be present when server explicitly sets 0; raw=${JSON.stringify(result)}`);
            assert.equal(result.ttl, 0,
                `${method}: ttl = ${result.ttl}, want 0`);
        }
    });

    // Absent / no guidance — server didn't configure WithListTTL, so the
    // wire MUST NOT carry a ttl field at all (clients fall back to
    // list_changed or their own heuristics).
    test('list-ttl-03: ttl is absent when server has no TTL configured', async () => {
        const sid = await initRawSession(URL_UNSET);
        for (const method of LIST_METHODS) {
            const result = await rawCall(URL_UNSET, sid, method);
            assert.ok(!('ttl' in result),
                `${method}: ttl field MUST be absent when server has no TTL; raw=${JSON.stringify(result)}`);
        }
    });

    // Coexistence — TTL doesn't disturb cursor pagination or the existing
    // payload arrays. Cheap regression guard against a future refactor
    // that might accidentally swap fields.
    test('list-ttl-04: ttl coexists with the list payload arrays', async () => {
        const sid = await initRawSession(URL_POSITIVE);

        const tools = await rawCall(URL_POSITIVE, sid, 'tools/list');
        assert.ok(Array.isArray(tools.tools), 'tools array must be present alongside ttl');
        assert.ok(tools.tools.length > 0, 'fixture should register at least one tool');

        const prompts = await rawCall(URL_POSITIVE, sid, 'prompts/list');
        assert.ok(Array.isArray(prompts.prompts), 'prompts array must be present alongside ttl');

        const resources = await rawCall(URL_POSITIVE, sid, 'resources/list');
        assert.ok(Array.isArray(resources.resources), 'resources array must be present alongside ttl');

        const templates = await rawCall(URL_POSITIVE, sid, 'resources/templates/list');
        assert.ok(Array.isArray(templates.resourceTemplates), 'resourceTemplates array must be present alongside ttl');
    });

    // Type guard — ttl MUST be a JSON number when present, never a
    // string or boolean. (Spec says `number`; a Go server bug encoding
    // it as `*string` would fail this.)
    test('list-ttl-05: ttl wire type is JSON number', async () => {
        const sid = await initRawSession(URL_POSITIVE);
        const result = await rawCall(URL_POSITIVE, sid, 'tools/list');
        assert.equal(typeof result.ttl, 'number',
            `ttl wire type = ${typeof result.ttl}, want "number"`);
        assert.ok(Number.isInteger(result.ttl),
            `ttl = ${result.ttl}, want integer`);
    });
});
