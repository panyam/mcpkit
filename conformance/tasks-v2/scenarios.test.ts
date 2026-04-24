/**
 * MCP Tasks v2 Conformance Scenarios (SEP-2557)
 *
 * Tests any MCP server that implements the Tasks v2 protocol as proposed
 * in SEP-2557 (https://github.com/modelcontextprotocol/modelcontextprotocol/pull/2557).
 *
 * SEP STATUS: Draft, targeted for 2026-06-30-RC milestone.
 * These tests are written ahead of the spec being finalized. They will
 * evolve as the SEP is refined. All scenarios should FAIL today — no
 * v2 server implementation exists yet (red-before-green).
 *
 * UPSTREAM PORTING NOTE: When porting to the conformance repo, these
 * individual test() calls should be consolidated into ~4 scenarios with
 * multiple ConformanceCheck entries each, per AGENTS.md: "one scenario
 * with many checks beats 10 scenarios with one check each." The current
 * structure is for readability during spec review. Each check will need
 * specReferences pointing to the relevant SEP-2557 sections.
 *
 * Key differences from v1 (spec 2025-11-25):
 *   - tasks/result REMOVED — result inlined into tasks/get
 *   - tasks/list REMOVED — sessions going away
 *   - No client `task` param — server decides unilaterally
 *   - TTL in seconds (not milliseconds)
 *   - requestState for stateless deployments
 *   - inputRequests/inputResponses inline in tasks/get (MRTR model)
 *   - tasks/cancel required (not optional)
 *   - No capability advertisement — tasks are core protocol
 *   - resultType: "task" discriminator on CreateTaskResult
 *   - "failed" status = JSON-RPC protocol error only; tool errors = "completed" + isError
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
 *   - confirm_delete — async, elicitation via inputRequests model
 */

import { describe, test, before, after } from 'node:test';
import { strict as assert } from 'node:assert';
import { Client, StreamableHTTPClientTransport } from '@modelcontextprotocol/client';
import { assertJsonRpcError, waitForTerminal, waitForStatus } from '../common/helpers.js';

const SERVER_URL = process.env.SERVER_URL || 'http://localhost:8080/mcp';

let client: Client;

before(async () => {
    const transport = new StreamableHTTPClientTransport(new URL(SERVER_URL));
    // v2: No tasks capability negotiation — tasks are core protocol.
    // Client still declares elicitation/sampling for inputRequests handling.
    client = new Client(
        { name: 'mcp-tasks-v2-conformance', version: '1.0.0' },
        { capabilities: { elicitation: {}, sampling: {} } }
    );
    await client.connect(transport);
});

after(async () => {
    await client.close();
});

// ============================================================================
// Helpers
// ============================================================================

/**
 * Call a tool via raw request. In v2, there is no client `task` param —
 * the server decides whether to create a task based on the tool's
 * configuration. Returns the raw result (may be CallToolResult or
 * CreateTaskResult depending on the tool).
 */
async function callTool(toolName: string, args: Record<string, unknown>): Promise<any> {
    return await client.request(
        {
            method: 'tools/call',
            params: { name: toolName, arguments: args },
        },
        {} as any
    );
}

/**
 * Call tasks/get with optional requestState and inputResponses.
 * This is the v2 consolidated polling endpoint.
 */
async function getTask(taskId: string, opts: { requestState?: string; inputResponses?: Record<string, any> } = {}): Promise<any> {
    const params: any = { taskId };
    if (opts.requestState !== undefined) {
        params.requestState = opts.requestState;
    }
    if (opts.inputResponses !== undefined) {
        params.inputResponses = opts.inputResponses;
    }
    return await client.request(
        { method: 'tasks/get', params },
        {} as any
    );
}

/**
 * Call tasks/cancel with optional requestState.
 */
async function cancelTask(taskId: string, requestState?: string): Promise<any> {
    const params: any = { taskId };
    if (requestState !== undefined) {
        params.requestState = requestState;
    }
    return await client.request(
        { method: 'tasks/cancel', params },
        {} as any
    );
}

/**
 * Assert that a CreateTaskResult has the v2-required resultType discriminator
 * and a valid task object.
 */
function assertCreateTaskResult(result: any, label: string) {
    assert.equal(result.resultType, 'task',
        `${label}: result.resultType must be "task"`);
    assert.ok(result.task, `${label}: should have task field`);
    assert.ok(result.task.taskId, `${label}: task should have taskId`);
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

describe('MCP Tasks v2 Conformance (SEP-2557)', () => {

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
        // No task field — sync tools don't create tasks.
        assert.ok(!result.task, 'sync tool should not have task field');
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
            !['completed', 'failed', 'cancelled'].includes(result.task.status),
            `initial status should be non-terminal, got ${result.task.status}`
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
        const taskId = result.task.taskId;

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
        const taskId = result.task.taskId;

        const terminal = await waitForTerminal(client, taskId);
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
        const taskId = result.task.taskId;

        const terminal = await waitForTerminal(client, taskId);
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
    // (e.g., server crash, internal error). The test server should provide
    // a `protocol_error_job` tool for this purpose.
    // ========================================================================
    test('v2-06: protocol error is failed with error field', async () => {
        const result = await callTool('protocol_error_job', {});
        assertCreateTaskResult(result, 'v2-06 create');
        const taskId = result.task.taskId;

        const terminal = await waitForTerminal(client, taskId);
        assert.equal(terminal.status, 'failed',
            'protocol error should have status: failed');
        assert.ok(terminal.error,
            'failed task should have inlined error field');
        // error should NOT have result
        assert.ok(!terminal.result,
            'failed task should not have result field');
    });

    // ========================================================================
    // Scenario 07: tasks/cancel (cooperative, required)
    //
    // In v2, tasks/cancel is REQUIRED (not optional like v1).
    // ========================================================================
    test('v2-07: tasks/cancel returns cancelled status', async () => {
        const result = await callTool('slow_compute', { seconds: 60, label: 'v2-cancel' });
        assertCreateTaskResult(result, 'v2-07 create');
        const taskId = result.task.taskId;

        const cancelled = await cancelTask(taskId);
        assert.equal(cancelled.status, 'cancelled', 'should be cancelled');

        // Confirm via tasks/get.
        const task = await getTask(taskId);
        assert.equal(task.status, 'cancelled', 'poll after cancel should show cancelled');
    });

    // ========================================================================
    // Scenario 08: tasks/cancel on terminal task returns error
    //
    // Per spec: -32602 (InvalidParams). Enforced from the start since
    // v2 is a new spec.
    // ========================================================================
    test('v2-08: cancel completed task returns error', async () => {
        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-cancel-done' });
        const taskId = result.task.taskId;
        await waitForTerminal(client, taskId);

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
        const taskId = result.task.taskId;
        await waitForTerminal(client, taskId);

        try {
            await client.request(
                { method: 'tasks/result', params: { taskId } },
                {} as any
            );
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
            await client.request(
                { method: 'tasks/list', params: {} },
                {} as any
            );
            assert.fail('should have thrown — tasks/list removed in v2');
        } catch (e: any) {
            assertJsonRpcError(e, -32601, 'tasks/list removed', true);
        }
    });

    // ========================================================================
    // Scenario 11: No tasks in initialize capabilities
    //
    // In v2, tasks are core protocol — not negotiated via capabilities.
    // The server SHOULD NOT advertise tasks in initialize.capabilities.
    //
    // NOTE: Accessing server capabilities via private fields is fragile.
    // The conformance repo may have a better mechanism for this check.
    // ========================================================================
    test('v2-11: tasks not advertised in initialize capabilities', async () => {
        // Access server capabilities — the mechanism varies by SDK version.
        const caps = (client as any)._serverCapabilities
            ?? (client as any).serverCapabilities
            ?? (client as any)._capabilities;
        if (caps) {
            assert.ok(!caps.tasks,
                'v2 server should not advertise tasks in capabilities (tasks are core protocol)');
        }
        // If we can't access capabilities, this check is a no-op.
        // The conformance repo may have a better mechanism.
    });

    // ========================================================================
    // Scenario 12: TTL field present and in seconds
    //
    // v2 aligns TTL with SEP-2549: seconds, not milliseconds.
    //
    // NOTE: TTL units are purely convention — the schema alone can't
    // distinguish seconds from milliseconds. The field may be renamed to
    // `ttlSeconds` (pending SEP-2549 resolution). This test checks the
    // value is reasonable for seconds but can't programmatically enforce
    // the unit. Servers with very long TTLs (e.g., 24h = 86400s) will
    // pass regardless of unit.
    // ========================================================================
    test('v2-12: TTL is present and reasonable for seconds', async () => {
        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-ttl' });
        const task = result.task;
        assert.ok(task.ttl !== undefined, 'task should have ttl');
        assert.ok(typeof task.ttl === 'number' && task.ttl > 0, 'ttl should be a positive number');
        // Best-effort heuristic: typical server defaults are 60-600 seconds.
        // If TTL > 10000, it's likely still using milliseconds (the most common
        // migration bug). This is convention, not schema-enforceable.
        assert.ok(task.ttl < 10000,
            `ttl should be in seconds (got ${task.ttl} — if >10000, likely milliseconds)`);
    });

    // ========================================================================
    // Scenario 13: Task not expired before TTL
    //
    // Servers MUST NOT expire before TTL elapses.
    // ========================================================================
    test('v2-13: task must not expire before TTL', async () => {
        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-ttl-guard' });
        assertCreateTaskResult(result, 'v2-13 create');
        const taskId = result.task.taskId;
        await waitForTerminal(client, taskId);

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
        const taskId = result.task.taskId;
        await waitForTerminal(client, taskId);

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
        const taskId = result.task.taskId;

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
    // so the client can match responses to requests.
    //
    // PROVISIONAL: inputRequests stays on tasks/get per Luca's confirmation.
    // inputResponses will likely move to a separate method (TBD).
    // ========================================================================
    test('v2-16: input_required task has inputRequests map in tasks/get', async () => {
        const result = await callTool('confirm_delete', { filename: 'v2-input.txt' });
        assertCreateTaskResult(result, 'v2-16 create');
        const taskId = result.task.taskId;

        // Wait for input_required.
        const task = await waitForStatus(client, taskId, 'input_required', 5000);
        assert.equal(task.status, 'input_required', 'should be input_required');

        // v2: inputRequests is a MAP, keyed by request identifier.
        assert.ok(task.inputRequests, 'input_required task should have inputRequests');
        assert.equal(typeof task.inputRequests, 'object',
            'inputRequests should be an object (map)');
        assert.ok(!Array.isArray(task.inputRequests),
            'inputRequests should be a map, not an array');

        const keys = Object.keys(task.inputRequests);
        assert.ok(keys.length > 0, 'inputRequests should have at least one entry');

        // Each request should have a method (e.g., elicitation/create).
        const firstKey = keys[0];
        const req = task.inputRequests[firstKey];
        assert.ok(req.method || req.type,
            'inputRequest should have a method or type field');
    });

    // ========================================================================
    // Scenario 17: inputResponses resumes task
    //
    // Client sends inputResponses as a MAP in a subsequent request to
    // provide the requested input. Keys must correspond to the inputRequests
    // keys. The task should resume.
    //
    // PROVISIONAL: inputResponses will likely move to a separate method
    // (name TBD). Currently using tasks/get per the SEP text, but this
    // scenario WILL NEED UPDATING when the delivery method is finalized.
    //
    // NOTE: The task can also return input_required again (e.g., if the
    // server sent multiple requests and the client only responded to one).
    // This scenario responds to all requests so the task should complete.
    // ========================================================================
    test('v2-17: inputResponses map resumes task', async () => {
        const result = await callTool('confirm_delete', { filename: 'v2-respond.txt' });
        assertCreateTaskResult(result, 'v2-17 create');
        const taskId = result.task.taskId;

        // Wait for input_required.
        const inputTask = await waitForStatus(client, taskId, 'input_required', 5000);
        assert.equal(inputTask.status, 'input_required', 'should be input_required');

        // Build inputResponses map — keys must match inputRequests keys.
        const requestKeys = Object.keys(inputTask.inputRequests || {});
        const responses: Record<string, any> = {};
        for (const key of requestKeys) {
            responses[key] = {
                action: 'accept',
                content: { confirm: true }
            };
        }

        const state = inputTask.requestState;
        const resumed = await getTask(taskId, {
            requestState: state,
            inputResponses: responses
        });

        // Task should have resumed — working, completed, or input_required again.
        assert.ok(
            ['working', 'completed', 'input_required'].includes(resumed.status),
            `task should have resumed, got ${resumed.status}`
        );

        // If not yet completed, wait for completion.
        if (resumed.status !== 'completed') {
            const terminal = await waitForTerminal(client, taskId);
            assert.equal(terminal.status, 'completed', 'task should complete after input');
        }
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
        const taskId = result.task.taskId;
        await waitForTerminal(client, taskId);
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

        if (result.task) {
            // Task path — must have resultType discriminator.
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
        const taskId = result.task.taskId;

        const terminal = await waitForTerminal(client, taskId);
        assertCompletedResult(terminal, 'v2-21');

        // related-task _meta should NOT be present — taskId is at root level.
        const meta = terminal.result?._meta;
        if (meta) {
            assert.ok(!meta['io.modelcontextprotocol/related-task'],
                'tasks/get inlined result should NOT include related-task _meta');
        }
    });

});
