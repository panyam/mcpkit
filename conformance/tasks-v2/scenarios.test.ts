/**
 * MCP Tasks v2 Conformance Scenarios (SEP-2663 / SEP-2322 / SEP-2575 / SEP-2243)
 *
 * Tests any MCP server that implements the Tasks v2 surface, evolving from
 * the original SEP-2557 draft to the current shape:
 *   - SEP-2663 — Tasks Extension (https://github.com/modelcontextprotocol/specification/pull/2663)
 *   - SEP-2322 — MRTR base types (inputRequests/inputResponses, requestState)
 *   - SEP-2575 — per-request capabilities via _meta.io.modelcontextprotocol/clientCapabilities
 *   - SEP-2243 — Mcp-Name HTTP response header
 *
 * UPSTREAM PORTING NOTE: When porting to the conformance repo, these
 * individual test() calls should be consolidated into ~4 scenarios with
 * multiple ConformanceCheck entries each, per AGENTS.md: "one scenario
 * with many checks beats 10 scenarios with one check each." The current
 * structure is for readability during spec review. Each check will need
 * specReferences pointing to the relevant SEP-2663 sections.
 *
 * Key wire-format differences from v1 (MCP spec 2025-11-25):
 *   - Tasks is an EXTENSION (io.modelcontextprotocol/tasks) advertised under
 *     capabilities.extensions — clients MUST declare support during initialize
 *     (or per-request via SEP-2575 _meta) for the server to accept the surface.
 *   - tasks/result REMOVED — result inlined into tasks/get's DetailedTask.
 *   - tasks/list REMOVED.
 *   - tasks/update is the new MRTR resume path (delivers inputResponses).
 *   - tasks/cancel returns an EMPTY {} ack (no task state).
 *   - No client `task` param — server decides unilaterally.
 *   - Wire fields renamed: ttlSeconds, pollIntervalMilliseconds. parentTaskId removed.
 *   - inputRequests is a MAP keyed by server-minted opaque ids; inputResponses
 *     mirrors the same keys via tasks/update.
 *   - resultType: "task" discriminator on CreateTaskResult; absence => sync ToolResult.
 *   - "failed" status = JSON-RPC protocol error only; tool errors = "completed" + isError.
 *   - Mcp-Name HTTP response header carries the new taskId on task-creating
 *     responses (SEP-2243).
 *
 * Usage:
 *   cd conformance && npm install
 *   SERVER_URL=http://localhost:8080/mcp npx tsx --test tasks-v2/scenarios.test.ts
 *
 * The server MUST register these tools:
 *   - greet — sync-only, returns "Hello, {name}!"
 *   - slow_compute — async, sleeps N seconds, returns result
 *   - failing_job — async, always fails after 1s (tool-level error, not protocol error)
 *   - protocol_error_job — async, fails with a JSON-RPC protocol error
 *   - confirm_delete — async, single-input elicitation via the SEP-2663 inputRequests / tasks/update flow
 *   - multi_input — async, fan-out tool that surfaces TWO simultaneous inputRequests (used by v2-29)
 */

import { describe, test, before, after } from 'node:test';
import { strict as assert } from 'node:assert';
import { Client, StreamableHTTPClientTransport } from '@modelcontextprotocol/client';
import { assertJsonRpcError } from '../common/helpers.js';

const SERVER_URL = process.env.SERVER_URL || 'http://localhost:8080/mcp';
const TASKS_EXTENSION_ID = 'io.modelcontextprotocol/tasks';

let client: Client;
let sessionId: string;          // raw session that DECLARES the tasks extension
let unsupportedSessionId: string; // raw session that does NOT declare the extension (for gating tests)
let nextId = 1;

// initRawSession runs the raw initialize handshake against SERVER_URL with the
// supplied capabilities and returns the session id from Mcp-Session-Id, after
// firing the notifications/initialized to make the session usable. We bypass
// the SDK because its built-in Zod schemas strip v2-only fields (result,
// error, inputRequests, etc.) from responses.
async function initRawSession(capabilities: Record<string, unknown>): Promise<string> {
    const initResp = await fetch(SERVER_URL, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Accept': 'application/json' },
        body: JSON.stringify({
            jsonrpc: '2.0', id: 'init-raw', method: 'initialize',
            params: {
                protocolVersion: '2025-11-25',
                clientInfo: { name: 'raw-init', version: '1.0' },
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
    const transport = new StreamableHTTPClientTransport(new URL(SERVER_URL));
    // v2: declare the io.modelcontextprotocol/tasks extension so the server
    // gates the task surface ON for this client. Also declare elicitation /
    // sampling so v1-style notifications round-trip.
    client = new Client(
        { name: 'mcp-tasks-v2-conformance', version: '1.0.0' },
        {
            capabilities: {
                elicitation: {},
                sampling: {},
                extensions: { [TASKS_EXTENSION_ID]: {} },
            },
        },
    );
    await client.connect(transport);

    // Two raw sessions: one that declares the tasks extension (used by every
    // happy-path scenario) and one that doesn't (used by the negative-gate
    // scenarios v2-23 / v2-25-no-meta).
    sessionId = await initRawSession({
        elicitation: {},
        sampling: {},
        extensions: { [TASKS_EXTENSION_ID]: {} },
    });
    unsupportedSessionId = await initRawSession({});
});

after(async () => {
    await client.close();
});

// ============================================================================
// Helpers
// ============================================================================

/**
 * Send a raw JSON-RPC request via fetch, bypassing SDK schema validation.
 * Parses SSE `data:` lines if the response is text/event-stream, or
 * plain JSON otherwise.
 */
async function rawRequest(
    method: string,
    params: any,
    opts: { sessionId?: string; meta?: any; headers?: Record<string, string> } = {},
): Promise<any> {
    const result = await rawRequestFull(method, params, opts);
    return result.result;
}

/**
 * Like rawRequest, but also returns the raw fetch Response so callers can
 * inspect transport-level headers. The opts.headers field is merged into
 * the outgoing fetch headers (after the harness defaults), letting tests
 * exercise SEP-2243 request-side routing headers (Mcp-Name / Mcp-Method)
 * or any other transport-level concern.
 */
async function rawRequestFull(
    method: string,
    params: any,
    opts: { sessionId?: string; meta?: any; headers?: Record<string, string> } = {},
): Promise<{ result: any; response: Response }> {
    const id = nextId++;
    const sid = opts.sessionId ?? sessionId;
    const requestParams = opts.meta ? { ...params, _meta: opts.meta } : params;
    const resp = await fetch(SERVER_URL, {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'Accept': 'text/event-stream, application/json',
            'Mcp-Session-Id': sid,
            ...(opts.headers ?? {}),
        },
        body: JSON.stringify({ jsonrpc: '2.0', id, method, params: requestParams }),
    });
    const ct = resp.headers.get('content-type') || '';
    let body: any;
    if (ct.includes('text/event-stream')) {
        // Parse SSE — find the first `data:` line with a JSON-RPC response.
        const text = await resp.text();
        for (const line of text.split('\n')) {
            const trimmed = line.trim();
            if (trimmed.startsWith('data:')) {
                const payload = trimmed.slice(5).trimStart();
                if (payload.startsWith('{')) {
                    const parsed = JSON.parse(payload);
                    if (parsed.id === id) {
                        body = parsed;
                        break;
                    }
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
    return { result: body.result, response: resp };
}

/**
 * Call a tool via raw request. In v2, there is no client `task` param —
 * the server decides whether to create a task based on the tool's
 * configuration. Returns the raw result (may be CallToolResult or
 * CreateTaskResult depending on the tool).
 */
async function callTool(toolName: string, args: Record<string, unknown>): Promise<any> {
    return rawRequest('tools/call', { name: toolName, arguments: args });
}

/**
 * Call tasks/get. Idempotent read of the v2 task surface — returns DetailedTask
 * with inlined result / error / inputRequests / requestState per status.
 * SEP-2663 moved input-response delivery off this method onto tasks/update;
 * see updateTask below.
 */
async function getTask(taskId: string, opts: { requestState?: string } = {}): Promise<any> {
    const params: any = { taskId };
    if (opts.requestState !== undefined) {
        params.requestState = opts.requestState;
    }
    return rawRequest('tasks/get', params);
}

/**
 * Call tasks/update — SEP-2663 MRTR resume path. Delivers inputResponses
 * keyed to whatever inputRequests the server emitted. Returns the empty
 * {} ack.
 */
async function updateTask(taskId: string, inputResponses: Record<string, any>, requestState?: string): Promise<any> {
    const params: any = { taskId, inputResponses };
    if (requestState !== undefined) {
        params.requestState = requestState;
    }
    return rawRequest('tasks/update', params);
}

/** Poll tasks/get until a terminal state via raw requests. */
async function waitForTerminal(taskId: string, timeoutMs = 10_000): Promise<any> {
    const start = Date.now();
    while (Date.now() - start < timeoutMs) {
        const task = await getTask(taskId);
        if (['completed', 'failed', 'cancelled'].includes(task.status)) {
            return task;
        }
        await new Promise(r => setTimeout(r, 200));
    }
    throw new Error(`Task ${taskId} did not reach terminal state within ${timeoutMs}ms`);
}

/** Poll tasks/get until a specific status or terminal. */
async function waitForStatus(taskId: string, status: string, timeoutMs = 10_000): Promise<any> {
    const start = Date.now();
    while (Date.now() - start < timeoutMs) {
        const task = await getTask(taskId);
        if (task.status === status || ['completed', 'failed', 'cancelled'].includes(task.status)) {
            return task;
        }
        await new Promise(r => setTimeout(r, 200));
    }
    throw new Error(`Task ${taskId} did not reach status ${status} within ${timeoutMs}ms`);
}

/**
 * Call tasks/cancel with optional requestState.
 */
async function cancelTask(taskId: string, requestState?: string): Promise<any> {
    const params: any = { taskId };
    if (requestState !== undefined) {
        params.requestState = requestState;
    }
    return rawRequest('tasks/cancel', params);
}

/**
 * Assert that a CreateTaskResult has the v2-required resultType discriminator
 * and the SEP-2663 flat task shape — taskId/status/ttlSeconds/... are at the
 * top level alongside resultType, NOT nested under a "task" wrapper.
 * (`Result & Task` per SEP-2663.)
 *
 * Also enforces the SEP-2663 forbidden-field rule: CreateTaskResult MUST NOT
 * carry `result`, `error`, or `inputRequests` — those belong on the
 * DetailedTask returned by tasks/get. And asserts createdAt / lastUpdatedAt
 * are ISO-8601 strings.
 */
function assertCreateTaskResult(result: any, label: string) {
    assert.equal(result.resultType, 'task',
        `${label}: result.resultType must be "task"`);
    assert.ok(!result.task,
        `${label}: SEP-2663 CreateTaskResult is a flat intersection; there must be no "task" wrapper key`);
    assert.ok(result.taskId, `${label}: should have top-level taskId`);
    assert.ok(result.status, `${label}: should have top-level status`);

    // SEP-2663: CreateTaskResult MUST NOT carry these — they live on
    // DetailedTask returned by tasks/get.
    assert.ok(!('result' in result),
        `${label}: CreateTaskResult MUST NOT carry result`);
    assert.ok(!('error' in result),
        `${label}: CreateTaskResult MUST NOT carry error`);
    assert.ok(!('inputRequests' in result),
        `${label}: CreateTaskResult MUST NOT carry inputRequests`);

    // SEP-2663 timestamps — both keys present and ISO-8601 formatted.
    // Regex is the pragmatic choice: Date.parse is too permissive (accepts
    // RFC-2822, "May 4 2026", etc.); new Date(s).toISOString() === s is too
    // strict (rejects valid +00:00 offsets and sub-second variations);
    // Temporal.Instant.from is Node 24+ experimental. If a stdlib ISO-8601
    // validator becomes broadly available, swap this regex for it.
    const iso8601 = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/;
    assert.match(result.createdAt, iso8601,
        `${label}: createdAt must be ISO-8601 (got ${result.createdAt})`);
    assert.match(result.lastUpdatedAt, iso8601,
        `${label}: lastUpdatedAt must be ISO-8601 (got ${result.lastUpdatedAt})`);
}

/**
 * Assert that a completed task has a well-formed inlined result.
 */
function assertCompletedResult(task: any, label: string) {
    assert.equal(task.status, 'completed', `${label}: status should be completed`);
    assert.ok(task.result, `${label}: completed task should have inlined result`);
    assert.ok(task.result.content, `${label}: result should have content`);
    assert.ok(Array.isArray(task.result.content), `${label}: content should be an array`);
    assert.ok(task.result.content.length > 0, `${label}: content should not be empty`);
}

// ============================================================================
// Scenarios
// ============================================================================

describe('MCP Tasks v2 Conformance (SEP-2663)', () => {

    // ========================================================================
    // Scenario 01: Sync tool call — no task created
    //
    // Tools without task support return inline results, same as v1.
    // ========================================================================
    test('v2-01: sync tool call returns immediately, no task', async () => {
        const result = await callTool('greet', { name: 'World' });
        const content = result.content as any[];
        assert.ok(content.length > 0, 'should have content');
        assert.equal(content[0].type, 'text');
        assert.equal(content[0].text, 'Hello, World!');
        // Sync tools don't create tasks. With the SEP-2663 flat CreateTaskResult
        // shape, the discriminator is `resultType` and the task fields would
        // be at the top level, so check both: no resultType:"task" and no
        // taskId at the root.
        assert.notEqual(result.resultType, 'task',
            'sync tool resultType must not be "task"');
        assert.ok(!result.taskId, 'sync tool should not have taskId at top level');
    });

    // ========================================================================
    // Scenario 02: Server-directed task creation
    //
    // In v2, the client does NOT send a `task` param. The server decides
    // to create a task based on the tool's configuration. The response is
    // a CreateTaskResult with resultType: "task" and a task object.
    // ========================================================================
    test('v2-02: server creates task without client task param', async () => {
        const result = await callTool('slow_compute', { seconds: 2, label: 'v2-create' });
        assertCreateTaskResult(result, 'v2-02');
        assert.ok(
            !['completed', 'failed', 'cancelled'].includes(result.status),
            `initial status should be non-terminal, got ${result.status}`
        );
    });

    // ========================================================================
    // Scenario 03: tasks/get returns working status
    //
    // Polling a non-terminal task returns its current status. If it has
    // already completed, the result must be inlined.
    // ========================================================================
    test('v2-03: tasks/get returns status for active task', async () => {
        const result = await callTool('slow_compute', { seconds: 3, label: 'v2-poll' });
        assertCreateTaskResult(result, 'v2-03 create');
        const taskId = result.taskId;

        const task = await getTask(taskId);
        assert.ok(task.taskId, 'should have taskId');
        assert.ok(task.status, 'should have status');

        // If the task already completed, verify inlined result.
        if (task.status === 'completed') {
            assertCompletedResult(task, 'v2-03 early-complete');
        }
    });

    // ========================================================================
    // Scenario 04: tasks/get returns completed + inlined result
    //
    // In v2, tasks/get returns the result inline when the task is completed.
    // There is no separate tasks/result method.
    // ========================================================================
    test('v2-04: tasks/get returns completed status with inlined result', async () => {
        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-result' });
        assertCreateTaskResult(result, 'v2-04 create');
        const taskId = result.taskId;

        const terminal = await waitForTerminal(taskId);
        assertCompletedResult(terminal, 'v2-04');
    });

    // ========================================================================
    // Scenario 05: Tool execution error — completed with isError: true
    //
    // In v2, tool execution errors (the tool ran but returned an error) are
    // represented as status: "completed" with result.isError: true. This
    // matches the v1 tool error handling semantics.
    //
    // The "failed" status is reserved for JSON-RPC protocol-level errors
    // (e.g., the server crashed, lost connection to the tool, etc.) and
    // inlines an `error` field (not `result`).
    // ========================================================================
    test('v2-05: tool execution error is completed with isError true', async () => {
        const result = await callTool('failing_job', {});
        assertCreateTaskResult(result, 'v2-05 create');
        const taskId = result.taskId;

        const terminal = await waitForTerminal(taskId);
        // Tool errors are "completed" with isError, NOT "failed".
        assert.equal(terminal.status, 'completed',
            'tool execution error should be completed (not failed)');
        assert.ok(terminal.result, 'should have inlined result');
        assert.equal(terminal.result.isError, true,
            'result should have isError: true for tool execution errors');
    });

    // ========================================================================
    // Scenario 06: Protocol-level error — failed with error field
    //
    // The "failed" status is used only for JSON-RPC protocol-level errors.
    // The task inlines an `error` field (not `result`).
    //
    // NOTE: This requires a tool that triggers a protocol-level failure
    // (e.g., server crash, internal error). The test server provides a
    // `protocol_error_job` tool that panics. Some SDKs (e.g., Python) make
    // this hard because they catch all exceptions and convert them to tool
    // errors — Go's panic recovery gives us clean control here.
    // ========================================================================
    test('v2-06: protocol error is failed with error field', async () => {
        const result = await callTool('protocol_error_job', {});
        assertCreateTaskResult(result, 'v2-06 create');
        const taskId = result.taskId;

        const terminal = await waitForTerminal(taskId);
        assert.equal(terminal.status, 'failed',
            'protocol error should have status: failed');
        assert.ok(terminal.error,
            'failed task should have inlined error field');
        // error should NOT have result
        assert.ok(!terminal.result,
            'failed task should not have result field');
    });

    // ========================================================================
    // Scenario 07: tasks/cancel returns empty ack; status settles to cancelled
    //
    // SEP-2663 changed the cancel response from a rich task envelope to an
    // empty {} ack — the client observes the resulting "cancelled" status
    // via the next tasks/get. This is more honest about the cooperative
    // nature of cancellation: the response only acknowledges the request,
    // not that the goroutine has stopped yet.
    // ========================================================================
    test('v2-07: tasks/cancel returns empty ack; status settles to cancelled', async () => {
        const result = await callTool('slow_compute', { seconds: 60, label: 'v2-cancel' });
        assertCreateTaskResult(result, 'v2-07 create');
        const taskId = result.taskId;

        const cancelAck = await cancelTask(taskId);
        // SEP-2663: cancel response carries no task state — only the SEP-2322
        // resultType:"complete" discriminator (added under v2-26).
        assert.deepEqual(cancelAck, { resultType: 'complete' },
            `tasks/cancel should return {resultType:"complete"} ack; got ${JSON.stringify(cancelAck)}`);

        // Status settles to cancelled — observe via tasks/get.
        const task = await getTask(taskId);
        assert.equal(task.status, 'cancelled', 'poll after cancel should show cancelled');
    });

    // ========================================================================
    // Scenario 08: tasks/cancel on terminal task returns error
    //
    // Per spec: SHOULD return -32602 (InvalidParams) when tasks/cancel is
    // called for an unknown / terminal taskId. Clarified upstream in
    // modelcontextprotocol/specification commit d963ad0.
    // ========================================================================
    test('v2-08: cancel completed task returns error', async () => {
        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-cancel-done' });
        const taskId = result.taskId;
        await waitForTerminal(taskId);

        try {
            await cancelTask(taskId);
            assert.fail('should have thrown an error');
        } catch (e: any) {
            assertJsonRpcError(e, -32602, 'cancel completed', true);
        }
    });

    // ========================================================================
    // Scenario 09: tasks/result method does not exist
    //
    // v2 removes tasks/result entirely. Servers MUST reject it.
    // -32601 (MethodNotFound) is mandated by JSON-RPC for unknown methods.
    //
    // NOTE: This negative test is useful for making the spec diff clear to
    // SDK implementors, even though a server could technically still support
    // it for backward compatibility (gated by protocol version).
    // ========================================================================
    test('v2-09: tasks/result is rejected (method removed in v2)', async () => {
        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-no-result' });
        const taskId = result.taskId;
        await waitForTerminal(taskId);

        try {
            await rawRequest('tasks/result', { taskId });
            assert.fail('should have thrown — tasks/result removed in v2');
        } catch (e: any) {
            assertJsonRpcError(e, -32601, 'tasks/result removed', true);
        }
    });

    // ========================================================================
    // Scenario 10: tasks/list method does not exist
    //
    // v2 removes tasks/list entirely. Same rationale as scenario 09.
    // ========================================================================
    test('v2-10: tasks/list is rejected (method removed in v2)', async () => {
        try {
            await rawRequest('tasks/list', {});
            assert.fail('should have thrown — tasks/list removed in v2');
        } catch (e: any) {
            assertJsonRpcError(e, -32601, 'tasks/list removed', true);
        }
    });

    // ========================================================================
    // Scenario 11: tasks live under capabilities.extensions, not the v1
    // capabilities.tasks slot
    //
    // SEP-2663: tasks is an extension, NOT a top-level capability. The server
    // MUST advertise it under capabilities.extensions[io.modelcontextprotocol/tasks]
    // and MUST NOT use the v1 capabilities.tasks slot.
    //
    // NOTE: Accessing server capabilities via private fields is fragile —
    // the mechanism varies by SDK version.
    // ========================================================================
    test('v2-11: tasks advertised under capabilities.extensions, not capabilities.tasks', async () => {
        const caps = (client as any)._serverCapabilities
            ?? (client as any).serverCapabilities
            ?? (client as any)._capabilities;
        if (!caps) {
            // SDK didn't expose capabilities — the conformance repo may have a
            // better mechanism. Skip rather than failing on missing access.
            return;
        }
        assert.ok(!caps.tasks,
            'v2 server must NOT use the v1 capabilities.tasks slot (use capabilities.extensions instead)');
        assert.ok(caps.extensions,
            'v2 server must advertise capabilities.extensions');
        assert.ok(caps.extensions[TASKS_EXTENSION_ID],
            `v2 server must advertise the ${TASKS_EXTENSION_ID} extension under capabilities.extensions`);
    });

    // ========================================================================
    // Scenario 12: ttlSeconds + pollIntervalMilliseconds wire-field renames
    //
    // SEP-2663 renamed two wire fields with units in the name (no convention
    // required):
    //   - `ttl` (milliseconds, by convention) → `ttlSeconds`
    //   - `pollInterval` (milliseconds, by convention) → `pollIntervalMilliseconds`
    //
    // The legacy keys MUST NOT appear on the v2 wire — clients that key off
    // them on a v2 server would silently see an unbounded TTL or a missing
    // poll hint.
    // ========================================================================
    test('v2-12: ttlSeconds + pollIntervalMilliseconds present (legacy v1 keys absent)', async () => {
        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-ttl' });
        // SEP-2663 flat CreateTaskResult: the renamed wire fields are at the
        // top level alongside resultType, not nested under a "task" wrapper.

        // ttlSeconds — required, positive, no legacy `ttl` key.
        assert.ok(result.ttlSeconds !== undefined,
            'CreateTaskResult should have ttlSeconds (SEP-2663 wire-field rename)');
        assert.ok(typeof result.ttlSeconds === 'number' && result.ttlSeconds > 0,
            'ttlSeconds should be a positive number');
        assert.ok(!('ttl' in result),
            'v2 CreateTaskResult MUST NOT carry the v1 `ttl` key (use ttlSeconds)');

        // pollIntervalMilliseconds — optional. When present it must be a
        // positive number and the legacy `pollInterval` key must NOT appear
        // on the v2 wire.
        if (result.pollIntervalMilliseconds !== undefined) {
            assert.ok(typeof result.pollIntervalMilliseconds === 'number' && result.pollIntervalMilliseconds > 0,
                'pollIntervalMilliseconds should be a positive number when present');
        }
        assert.ok(!('pollInterval' in result),
            'v2 CreateTaskResult MUST NOT carry the v1 `pollInterval` key (use pollIntervalMilliseconds)');
    });

    // ========================================================================
    // Scenario 13: Task not expired before TTL
    //
    // Servers MUST NOT expire before TTL elapses.
    // ========================================================================
    test('v2-13: task must not expire before TTL', async () => {
        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-ttl-guard' });
        assertCreateTaskResult(result, 'v2-13 create');
        const taskId = result.taskId;
        await waitForTerminal(taskId);

        // Task should still be accessible well before TTL (which is in seconds).
        await new Promise(r => setTimeout(r, 500));
        const task = await getTask(taskId);
        assert.ok(task.taskId, 'task should still exist before TTL expires');
    });

    // ========================================================================
    // Scenario 14: requestState returned by server
    //
    // v2 adds requestState for stateless deployments. The server MAY return
    // a requestState in tasks/get responses.
    // ========================================================================
    test('v2-14: tasks/get response may include requestState', async () => {
        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-reqstate' });
        const taskId = result.taskId;
        await waitForTerminal(taskId);

        const task = await getTask(taskId);
        // requestState is optional — if present, must be a string.
        if (task.requestState !== undefined) {
            assert.equal(typeof task.requestState, 'string',
                'requestState should be a string');
            assert.ok(task.requestState.length > 0,
                'requestState should be non-empty if present');
        }
    });

    // ========================================================================
    // Scenario 15: requestState echoed by client
    //
    // If the server returns requestState, the client MUST echo it back
    // in subsequent tasks/get and tasks/cancel requests.
    // ========================================================================
    test('v2-15: client echoes requestState in subsequent requests', async () => {
        const result = await callTool('slow_compute', { seconds: 2, label: 'v2-reqstate-echo' });
        const taskId = result.taskId;

        const first = await getTask(taskId);
        const state = first.requestState;

        if (state) {
            const second = await getTask(taskId, { requestState: state });
            assert.ok(second.taskId, 'should still return task info');
            if (second.requestState !== undefined) {
                assert.equal(typeof second.requestState, 'string',
                    'updated requestState should be a string');
            }
        }
        // If no requestState, the server doesn't use it — skip.
    });

    // ========================================================================
    // Scenario 16: inputRequests via tasks/get
    //
    // When a task needs input (elicitation/sampling), v2 returns
    // status: input_required with an inputRequests map in tasks/get.
    // This replaces the v1 side-channel via tasks/result.
    //
    // inputRequests is a MAP (not an array) — keys identify each request
    // so the client can match responses to requests. The companion
    // inputResponses path landed as the dedicated tasks/update method —
    // exercised by v2-17 below.
    // ========================================================================
    test('v2-16: input_required task has inputRequests map in tasks/get', async () => {
        const result = await callTool('confirm_delete', { filename: 'v2-input.txt' });
        assertCreateTaskResult(result, 'v2-16 create');
        const taskId = result.taskId;

        // Wait for input_required.
        const task = await waitForStatus(taskId, 'input_required', 5000);
        assert.equal(task.status, 'input_required', 'should be input_required');

        // v2: inputRequests is a MAP, keyed by request identifier.
        assert.ok(task.inputRequests, 'input_required task should have inputRequests');
        assert.ok(typeof task.inputRequests === 'object' && task.inputRequests !== null,
            'inputRequests should be a non-null object (map)');

        const keys = Object.keys(task.inputRequests);
        assert.ok(keys.length > 0, 'inputRequests should have at least one entry');

        // Each request should have a method (e.g., elicitation/create).
        const firstKey = keys[0];
        const req = task.inputRequests[firstKey];
        assert.ok(req.method || req.type,
            'inputRequest should have a method or type field');
    });

    // ========================================================================
    // Scenario 17: tasks/update delivers inputResponses; task resumes
    //
    // SEP-2663 finalized the MRTR resume path: the client sends inputResponses
    // via the new tasks/update method (NOT on tasks/get). The server matches
    // keys to the previously-emitted inputRequests, hands the payloads to the
    // waiting goroutine, and the task transitions back to working (or
    // directly to terminal if the tool finishes immediately after).
    // tasks/update returns an empty {} ack — observe the resulting status
    // via the next tasks/get.
    // ========================================================================
    test('v2-17: tasks/update inputResponses resume task', async () => {
        const result = await callTool('confirm_delete', { filename: 'v2-respond.txt' });
        assertCreateTaskResult(result, 'v2-17 create');
        const taskId = result.taskId;

        // Wait for input_required + populated inputRequests.
        const inputTask = await waitForStatus(taskId, 'input_required', 5000);
        assert.equal(inputTask.status, 'input_required', 'should be input_required');
        assert.ok(inputTask.inputRequests && Object.keys(inputTask.inputRequests).length > 0,
            'input_required task must surface inputRequests via tasks/get');

        // Build inputResponses keyed by the server-minted opaque ids. Echo
        // the requestState the server returned so stateless deployments
        // can pick the request back up.
        const responses: Record<string, any> = {};
        for (const key of Object.keys(inputTask.inputRequests)) {
            responses[key] = { action: 'accept', content: { confirm: true } };
        }

        const ack = await updateTask(taskId, responses, inputTask.requestState);
        // SEP-2663: ack carries no task state — only the SEP-2322
        // resultType:"complete" discriminator (covered by v2-26).
        assert.deepEqual(ack, { resultType: 'complete' },
            `tasks/update should return {resultType:"complete"} ack; got ${JSON.stringify(ack)}`);

        // Server-side goroutine resumes — status will settle to terminal
        // (or back to input_required if the tool emits another round).
        const terminal = await waitForTerminal(taskId);
        assert.equal(terminal.status, 'completed',
            `task should complete after tasks/update; got ${terminal.status}`);
    });

    // ========================================================================
    // Scenario 18: Status notification with DetailedTask (optional)
    //
    // v2 status notifications include the full DetailedTask, so terminal
    // notifications have inlined result/error. Notifications are optional,
    // but if sent, they MUST be delivered on the tasks/get SSE response
    // stream (not testable from this client-side suite).
    // ========================================================================
    test('v2-18: status notifications include DetailedTask if sent', async () => {
        const statusEvents: any[] = [];

        client.setNotificationHandler('notifications/tasks/status', (notification: any) => {
            statusEvents.push(notification.params);
        });

        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-notify' });
        const taskId = result.taskId;
        await waitForTerminal(taskId);
        await new Promise(r => setTimeout(r, 500));

        if (statusEvents.length > 0) {
            for (const evt of statusEvents) {
                assert.ok(evt.taskId, 'status notification should have taskId');
                assert.ok(evt.status, 'status notification should have status');
            }

            // Terminal notifications should include inlined result (v2 DetailedTask).
            const terminal = statusEvents.filter(
                (e: any) => e.taskId === taskId && e.status === 'completed'
            );
            if (terminal.length > 0) {
                const last = terminal[terminal.length - 1];
                assert.ok(last.result,
                    'v2 completed notification should include inlined result');
            }
        }
        // No assertion on count — notifications are optional.
    });

    // ========================================================================
    // Scenario 19: No client `task` param needed
    //
    // In v2, execution.taskSupport is removed. The server decides whether
    // to create a task. The client just calls tools/call normally.
    // ========================================================================
    test('v2-19: tools/call without task param creates task for async tools', async () => {
        const result = await callTool('failing_job', {});
        assertCreateTaskResult(result, 'v2-19');
    });

    // ========================================================================
    // Scenario 20: Immediate result shortcut
    //
    // v2 allows servers to return an immediate result even for task-capable
    // tools when the operation completes fast enough. The server MAY return
    // a CallToolResult (no task) or a CreateTaskResult (with task).
    // Both are valid responses.
    // ========================================================================
    test('v2-20: server may return immediate result for fast operations', async () => {
        const result = await callTool('slow_compute', { seconds: 0, label: 'v2-instant' });

        if (result.resultType === 'task') {
            // Task path — must have the SEP-2663 flat shape.
            assertCreateTaskResult(result, 'v2-20 task path');
        } else {
            // Immediate result path — verify content.
            assert.ok(result.content, 'immediate result should have content');
            assert.ok(Array.isArray(result.content), 'content should be an array');
        }
    });

    // ========================================================================
    // Scenario 21: related-task _meta NOT on tasks/get inlined results
    //
    // With tasks/result removed in v2, the related-task metadata is
    // unnecessary — the taskId is already at the root of the tasks/get
    // response. Verify its absence.
    // ========================================================================
    test('v2-21: tasks/get inlined result does not include related-task _meta', async () => {
        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-no-meta' });
        assertCreateTaskResult(result, 'v2-21 create');
        const taskId = result.taskId;

        const terminal = await waitForTerminal(taskId);
        assertCompletedResult(terminal, 'v2-21');

        // related-task _meta MUST be absent — taskId is at root level,
        // so the metadata is redundant. Verify absence, not just "if present."
        const meta = terminal.result?._meta;
        const related = meta?.['io.modelcontextprotocol/related-task'];
        assert.ok(!related,
            'tasks/get inlined result MUST NOT include related-task _meta');
    });

    // ========================================================================
    // Scenario 22: tasks/* rejected when extension not negotiated
    //
    // SEP-2663: tasks/get / tasks/update / tasks/cancel MUST NOT exist for
    // a session that did not declare the io.modelcontextprotocol/tasks
    // extension during initialize. Servers return -32601 (MethodNotFound)
    // so unsupported clients don't see a tasks surface they didn't ask for.
    //
    // This scenario uses a SECOND session (unsupportedSessionId, set up in
    // before()) that omitted the extension declaration.
    // ========================================================================
    test('v2-22: tasks/* return -32601 when extension not negotiated', async () => {
        const cases: Array<{ method: string; params: any }> = [
            { method: 'tasks/get', params: { taskId: 'any-id' } },
            { method: 'tasks/update', params: { taskId: 'any-id', inputResponses: {} } },
            { method: 'tasks/cancel', params: { taskId: 'any-id' } },
        ];
        for (const tc of cases) {
            try {
                await rawRequest(tc.method, tc.params, { sessionId: unsupportedSessionId });
                assert.fail(`${tc.method} should fail without extension negotiation`);
            } catch (e: any) {
                assertJsonRpcError(e, -32601, `${tc.method} without ext`, true);
            }
        }
    });

    // ========================================================================
    // Scenario 23: tools/call without extension does NOT create a task
    //
    // SEP-2663: a client that did not negotiate the extension still gets to
    // call task-eligible tools — the server falls through to synchronous
    // execution and returns a plain ToolResult. SEP-2322: that ToolResult
    // carries resultType:"complete" so polymorphic dispatch on the wire is
    // uniform. The server MUST NOT return CreateTaskResult (resultType:"task")
    // here.
    // ========================================================================
    test('v2-23: tools/call without extension returns sync ToolResult (resultType:"complete", no task)', async () => {
        const result = await rawRequest('tools/call',
            { name: 'slow_compute', arguments: { seconds: 0, label: 'v2-23' } },
            { sessionId: unsupportedSessionId },
        );
        // SEP-2322: sync ToolResult carries resultType:"complete" (not "task").
        assert.equal(result.resultType, 'complete',
            `sync ToolResult.resultType = ${result.resultType}, want "complete"`);
        // SEP-2663 flat shape: a CreateTaskResult would have taskId at the top
        // level; a sync ToolResult does not. Belt-and-braces: also reject any
        // legacy "task" wrapper key that some servers might still emit.
        assert.ok(!result.taskId,
            `tools/call without extension MUST NOT carry CreateTaskResult shape; got taskId=${result.taskId}`);
        assert.ok(!result.task,
            `tools/call without extension MUST NOT carry the legacy nested task envelope; got ${JSON.stringify(result.task)}`);
        // Sync ToolResult shape: should have content[].
        assert.ok(result.content,
            `expected sync ToolResult with content[]; got ${JSON.stringify(result)}`);
    });

    // ========================================================================
    // Scenario 24: SEP-2243 — server tolerates Mcp-Method / Mcp-Name request
    // headers
    //
    // SEP-2243 defines Mcp-Method and Mcp-Name as REQUEST headers (client →
    // server) used by HTTP infrastructure (proxies, gateways, observability)
    // to route or shape JSON-RPC traffic without parsing the body:
    //
    //   - Mcp-Method: <jsonrpc-method> — set on every JSON-RPC request so
    //     edge routers can dispatch by method.
    //   - Mcp-Name: <task-id> — set on resume operations (tasks/get,
    //     tasks/update, tasks/cancel) so routers can stick the request to
    //     the node owning the task.
    //
    // The headers are informational. The JSON-RPC body is authoritative —
    // the server MUST tolerate them, MUST NOT require them, and MUST NOT
    // change behavior based on them. These scenarios assert that tolerance.
    //
    // (Whether the server *also* echoes the headers on responses for
    // downstream observability is implementation-defined and out of scope
    // for SEP-2243 conformance — see server-side regression tests for that
    // behavior.)
    // ========================================================================
    test('v2-24: server tolerates Mcp-Method request header on tools/call', async () => {
        const result = await rawRequest('tools/call',
            { name: 'greet', arguments: { name: 'sep-2243' } },
            { headers: { 'Mcp-Method': 'tools/call' } },
        );
        // Body is authoritative; header is informational. Response should
        // be a normal sync ToolResult with the expected content.
        assert.equal(result.resultType, 'complete',
            'sync ToolResult resultType must be "complete" regardless of Mcp-Method header');
        assert.ok(Array.isArray(result.content) && result.content.length > 0,
            'sync ToolResult should still return content[]');
        assert.equal(result.content[0].text, 'Hello, sep-2243!',
            'tool result should be unaffected by the Mcp-Method header');
    });

    test('v2-24b: server tolerates Mcp-Method + Mcp-Name request headers on tasks/get', async () => {
        // Drive a task so we have a real taskId to route on.
        const created = await callTool('slow_compute', { seconds: 60, label: 'v2-24b' });
        assertCreateTaskResult(created, 'v2-24b setup');
        const taskId = created.taskId;

        // tasks/get with both routing headers set — server MUST treat the
        // body as authoritative and respond normally.
        const got = await rawRequest('tasks/get', { taskId }, {
            headers: {
                'Mcp-Method': 'tasks/get',
                'Mcp-Name': taskId,
            },
        });
        assert.equal(got.taskId, taskId,
            'tasks/get response MUST resolve the body taskId regardless of routing headers');
        assert.ok(got.status,
            'tasks/get must still return a status when Mcp-Method/Mcp-Name request headers are set');

        await cancelTask(taskId);
    });

    test('v2-24c: server ignores Mcp-Method header that disagrees with body method', async () => {
        // A misconfigured proxy might forward a stale Mcp-Method header that
        // disagrees with the body. SEP-2243 says the body is authoritative,
        // so the server MUST execute the body's method (tools/call here)
        // and not attempt to honor the header's claim (tasks/get).
        const result = await rawRequest('tools/call',
            { name: 'greet', arguments: { name: 'header-mismatch' } },
            { headers: { 'Mcp-Method': 'tasks/get' } },
        );
        assert.equal(result.resultType, 'complete',
            'server MUST dispatch on the body method, not the Mcp-Method header');
        assert.ok(Array.isArray(result.content),
            'server MUST return the body method\'s response shape (sync ToolResult)');
        assert.equal(result.content[0].text, 'Hello, header-mismatch!',
            'tool result must reflect the body method, not the header');
    });

    // ========================================================================
    // Scenario 25: SEP-2575 per-request capability override
    //
    // A client that did NOT declare the extension at session level can opt
    // into task creation for a single tools/call by including the extension
    // under _meta.io.modelcontextprotocol/clientCapabilities.extensions.
    // Per-request opt-in is additive — it cannot revoke a session-level
    // declaration and only applies to the current request.
    // ========================================================================
    test('v2-25: per-request _meta opt-in produces CreateTaskResult', async () => {
        const result = await rawRequest('tools/call',
            { name: 'slow_compute', arguments: { seconds: 1, label: 'v2-25' } },
            {
                sessionId: unsupportedSessionId, // session-level: no extension
                meta: {
                    'io.modelcontextprotocol/clientCapabilities': {
                        extensions: { [TASKS_EXTENSION_ID]: {} },
                    },
                },
            },
        );
        assertCreateTaskResult(result, 'v2-25 per-request opt-in');
    });

    // ========================================================================
    // Scenario 26: SEP-2322 resultType discriminator on non-task responses
    //
    // SEP-2322 requires every non-task JSON-RPC response on the tools+tasks
    // surface to carry a resultType discriminator so clients can dispatch
    // sync vs task vs multi-round uniformly without inspecting the payload.
    // Task-creation responses use resultType:"task" (covered by v2-02 +
    // assertCreateTaskResult); every other response — sync tools/call,
    // tasks/get, tasks/update, tasks/cancel — MUST carry resultType:"complete".
    //
    // This scenario batches the four non-task assertions in one place so a
    // server that misses one fails loudly rather than passing the unrelated
    // scenario it slipped through.
    // ========================================================================
    test('v2-26: non-task responses carry resultType:"complete" (SEP-2322)', async () => {
        // Sync tools/call — extension declared but the tool isn't async.
        const sync = await callTool('greet', { name: 'v2-26' });
        assert.equal(sync.resultType, 'complete',
            `sync tools/call resultType = ${sync.resultType}, want "complete"`);

        // tasks/get — drive a fast task to completion and read the response.
        const created = await callTool('slow_compute', { seconds: 0, label: 'v2-26' });
        assertCreateTaskResult(created, 'v2-26 setup');
        const taskId = created.taskId;
        await waitForTerminal(taskId);
        const got = await getTask(taskId);
        assert.equal(got.resultType, 'complete',
            `tasks/get resultType = ${got.resultType}, want "complete"`);

        // tasks/cancel — empty ack on a (already-terminal) task should still
        // reject with -32602; pick a fresh long-running task to cancel cleanly.
        const longLived = await callTool('slow_compute', { seconds: 60, label: 'v2-26-cancel' });
        const cancelAck = await cancelTask(longLived.taskId);
        assert.equal(cancelAck.resultType, 'complete',
            `tasks/cancel ack.resultType = ${cancelAck.resultType}, want "complete"`);

        // tasks/update — bogus key on a non-terminal task gets a clean ack.
        const elicit = await callTool('confirm_delete', { filename: 'v2-26.txt' });
        const elicitTaskId = elicit.taskId;
        await waitForStatus(elicitTaskId, 'input_required', 5000);
        const updateAck = await updateTask(elicitTaskId, { 'unknown-key': { ignored: true } });
        assert.equal(updateAck.resultType, 'complete',
            `tasks/update ack.resultType = ${updateAck.resultType}, want "complete"`);
        // Clean up the parked elicit task.
        await cancelTask(elicitTaskId);
    });

    // ========================================================================
    // Scenario 27: SEP-2663 strong-consistency / durable-create
    //
    // "A server MUST NOT return CreateTaskResult until the task is durably
    // created — that is, until a tasks/get for the returned taskId would
    // resolve."
    //
    // Issue tools/call → take the returned taskId → immediately issue
    // tasks/get with no sleep / poll. The server MUST resolve, not -32602.
    // A naive implementation that creates the task in the background goroutine
    // (after returning CreateTaskResult to the client) would fail this test.
    // ========================================================================
    test('v2-27: tasks/get immediately after CreateTaskResult must resolve', async () => {
        const created = await callTool('slow_compute', { seconds: 60, label: 'v2-27' });
        assertCreateTaskResult(created, 'v2-27 create');

        // No await/sleep between the create and the get — codifies the
        // strong-consistency ordering.
        const got = await getTask(created.taskId);
        assert.equal(got.taskId, created.taskId,
            'immediate tasks/get after CreateTaskResult must resolve (SEP-2663 durable-create)');

        // Cleanup so we don't leak a 60-second task.
        await cancelTask(created.taskId);
    });

    // ========================================================================
    // Scenario 28: SEP-2663 stale requestState tolerance
    //
    // "Servers MUST tolerate receiving a stale or outdated value gracefully."
    //
    // Each tasks/get mints a fresh requestState (different exp). After two
    // tasks/get calls, the first token is "stale" (a newer one exists) but
    // still within its TTL — echoing it MUST succeed, not -32602.
    //
    // No-op for servers running in legacy plaintext mode (requestState ==
    // taskID, every echo is identical) — the assertion still holds because
    // the same plaintext token round-trips both times.
    // ========================================================================
    test('v2-28: stale-but-valid requestState is tolerated', async () => {
        const created = await callTool('slow_compute', { seconds: 60, label: 'v2-28' });
        assertCreateTaskResult(created, 'v2-28 create');
        const taskId = created.taskId;

        const first = await getTask(taskId);
        const tokenA = first.requestState;
        if (!tokenA) {
            // Server didn't issue requestState at all — nothing to test.
            // (Conformance allows requestState to be absent per SEP-2322.)
            await cancelTask(taskId);
            return;
        }

        // Second call mints a fresh token (potentially with a newer exp),
        // making tokenA stale on a server that signs with embedded expiry.
        const second = await getTask(taskId, { requestState: tokenA });
        assert.ok(second.taskId, 'second tasks/get should succeed when echoing tokenA');

        // Now echo the older tokenA again — server MUST accept stale-but-valid.
        const stale = await getTask(taskId, { requestState: tokenA });
        assert.equal(stale.taskId, taskId,
            'stale-but-valid requestState MUST be tolerated (SEP-2663)');

        await cancelTask(taskId);
    });

    // ========================================================================
    // Scenario 29: SEP-2663 partial inputResponses fulfillment
    //
    // A tool that emits multiple simultaneous input requests parks the task
    // in input_required with multiple keys in inputRequests. The client MAY
    // satisfy them one at a time. SEP-2663:
    //   - tasks/update with a subset of keys MUST be accepted as a clean
    //     {resultType:"complete"} ack;
    //   - the task MUST stay in input_required until every pending request
    //     has been answered;
    //   - tasks/get after a partial update MUST surface only the still-
    //     pending keys in inputRequests (the answered key is gone).
    //
    // Requires the multi_input fixture, which fires two parallel TaskElicit
    // calls so two keys are pending at once.
    // ========================================================================
    test('v2-29: tasks/update with a subset of keys leaves the rest pending', async () => {
        const created = await callTool('multi_input', {});
        assertCreateTaskResult(created, 'v2-29 create');
        const taskId = created.taskId;

        // Wait for input_required with both keys pending. The fan-out tool
        // races two TaskElicits, so we may briefly see one key before the
        // second arrives — poll until we see two.
        let inputTask: any;
        const start = Date.now();
        while (Date.now() - start < 5000) {
            inputTask = await getTask(taskId);
            if (inputTask.status === 'input_required'
                && inputTask.inputRequests
                && Object.keys(inputTask.inputRequests).length >= 2) {
                break;
            }
            await new Promise(r => setTimeout(r, 100));
        }
        assert.equal(inputTask.status, 'input_required',
            'task with two parallel elicits should be input_required');
        const keys = Object.keys(inputTask.inputRequests);
        assert.ok(keys.length >= 2,
            `multi_input must surface 2 inputRequests; got ${keys.length}: ${JSON.stringify(keys)}`);

        const [firstKey, secondKey] = keys;

        // Answer only the first key. ack is the SEP-2322 empty-complete shape.
        const firstAck = await updateTask(taskId, {
            [firstKey]: { action: 'accept', content: { name: 'partial-1', confirm: true } },
        });
        assert.equal(firstAck.resultType, 'complete',
            'partial tasks/update ack must carry resultType:"complete"');

        // Status must still be input_required, with only the second key
        // remaining in inputRequests.
        const afterFirst = await getTask(taskId);
        assert.equal(afterFirst.status, 'input_required',
            'task must stay input_required while the second input is still pending');
        assert.ok(afterFirst.inputRequests,
            'tasks/get after partial update must still surface inputRequests');
        const remaining = Object.keys(afterFirst.inputRequests);
        assert.ok(remaining.includes(secondKey),
            `the unanswered key must remain in inputRequests; got ${JSON.stringify(remaining)}`);
        assert.ok(!remaining.includes(firstKey),
            `the answered key must be removed from inputRequests; still saw ${firstKey}`);

        // Answer the second key — task resumes and runs to completion.
        const secondAck = await updateTask(taskId, {
            [secondKey]: { action: 'accept', content: { name: 'partial-2', confirm: true } },
        });
        assert.equal(secondAck.resultType, 'complete',
            'final tasks/update ack must carry resultType:"complete"');

        const terminal = await waitForTerminal(taskId);
        assert.equal(terminal.status, 'completed',
            `task must complete after both inputs are satisfied; got ${terminal.status}`);
    });

    // ========================================================================
    // Scenario 30: tasks/get with unknown taskId returns -32602
    //
    // Symmetry with v2-08 (tasks/cancel on unknown / terminal taskId returns
    // -32602). SEP-2663's invalid-taskId rule applies to every method on
    // the tasks surface, not just cancel — a tasks/get against an id the
    // server doesn't know about MUST surface as an InvalidParams error so
    // the client doesn't silently get an empty/well-formed envelope it can
    // mistake for a legitimate state.
    // ========================================================================
    test('v2-30: tasks/get with unknown taskId returns -32602', async () => {
        try {
            await rawRequest('tasks/get', { taskId: 'nonexistent-12345' });
            assert.fail('tasks/get with invalid taskId must return error');
        } catch (e: any) {
            assertJsonRpcError(e, -32602, 'tasks/get unknown id', true);
        }
    });

    // ========================================================================
    // Scenario 31: legacy v1 `task` param on tools/call is ignored
    //
    // SEP-2663 removed the client-supplied `task` hint param — the server
    // decides whether to create a task. A v1 client that still sends
    // `task: { ttl, pollInterval }` MUST get a clean response: the server
    // either ignores the param entirely (sync tool → sync ToolResult) or
    // makes its own task-creation decision (server-async tool → CreateTaskResult).
    // The server MUST NOT error out, AND it MUST NOT promote a sync tool to
    // a task just because the client sent the legacy hint.
    // ========================================================================
    test('v2-31: legacy `task` param is ignored on sync tool', async () => {
        const result = await rawRequest('tools/call', {
            name: 'greet',
            arguments: { name: 'legacy-hint' },
            // Legacy v1 hint that the server should silently ignore.
            task: { ttl: 60000, pollInterval: 100 },
        });
        // Server must not have errored (rawRequest would have thrown).
        assert.notEqual(result.resultType, 'task',
            'legacy `task` param MUST NOT promote a sync tool to a task');
        assert.ok(!result.taskId,
            'sync tool with legacy `task` param MUST NOT carry a taskId');
        // Sanity: the sync result still arrives.
        assert.ok(Array.isArray(result.content) && result.content.length > 0,
            'sync tool should still return content[]');
    });

});
