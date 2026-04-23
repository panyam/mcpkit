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
 *
 * Usage:
 *   cd conformance && npm install
 *   SERVER_URL=http://localhost:8080/mcp npx tsx --test tasks-v2/scenarios.test.ts
 *
 * The server MUST register these tools:
 *   - greet — sync-only, returns "Hello, {name}!"
 *   - slow_compute — async, sleeps N seconds, returns result
 *   - failing_job — async, always fails after 1s
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
async function getTask(taskId: string, opts: { requestState?: string; inputResponses?: any[] } = {}): Promise<any> {
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
    // a CreateTaskResult with a task object.
    // ========================================================================
    test('v2-02: server creates task without client task param', async () => {
        const result = await callTool('slow_compute', { seconds: 2, label: 'v2-create' });
        assert.ok(result.task, 'task-capable tool should return task');
        assert.ok(result.task.taskId, 'task should have taskId');
        assert.ok(
            !['completed', 'failed', 'cancelled'].includes(result.task.status),
            `initial status should be non-terminal, got ${result.task.status}`
        );
    });

    // ========================================================================
    // Scenario 03: tasks/get returns working status
    //
    // Polling a non-terminal task returns its current status.
    // ========================================================================
    test('v2-03: tasks/get returns working status for active task', async () => {
        const result = await callTool('slow_compute', { seconds: 3, label: 'v2-poll' });
        const taskId = result.task.taskId;

        const task = await getTask(taskId);
        assert.ok(task.taskId, 'should have taskId');
        assert.ok(task.status, 'should have status');
        // May still be working or could have completed if fast.
        assert.ok(
            ['working', 'completed'].includes(task.status),
            `status should be working or completed, got ${task.status}`
        );
    });

    // ========================================================================
    // Scenario 04: tasks/get returns completed + inlined result
    //
    // In v2, tasks/get returns the result inline when the task is completed.
    // There is no separate tasks/result method.
    // ========================================================================
    test('v2-04: tasks/get returns completed status with inlined result', async () => {
        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-result' });
        const taskId = result.task.taskId;

        const terminal = await waitForTerminal(client, taskId);
        assert.equal(terminal.status, 'completed', 'should be completed');
        // v2: result is inlined in the tasks/get response.
        assert.ok(terminal.result, 'completed task should have inlined result');
        assert.ok(terminal.result.content, 'result should have content');
        assert.ok(Array.isArray(terminal.result.content), 'content should be an array');
        assert.ok(terminal.result.content.length > 0, 'content should not be empty');
    });

    // ========================================================================
    // Scenario 05: tasks/get returns failed + inlined error
    //
    // SEP-2557 defines FailedTask with an `error` field. However, the exact
    // shape is still being finalized. This test checks for the `error` field
    // as the canonical form per SEP-2557's FailedTask type.
    //
    // OPEN QUESTION @LucaButBoring: Is `error` the definitive field name for
    // FailedTask, or could it also be `result` with `isError: true`
    // (matching v1 semantics)? Current assertion checks `error` first.
    // ========================================================================
    test('v2-05: tasks/get returns failed status with inlined error', async () => {
        const result = await callTool('failing_job', {});
        const taskId = result.task.taskId;

        const terminal = await waitForTerminal(client, taskId);
        assert.equal(terminal.status, 'failed', 'should be failed');
        // v2 FailedTask should have an `error` field per SEP-2557.
        assert.ok(terminal.error,
            'failed task should have inlined error field (FailedTask type)');
    });

    // ========================================================================
    // Scenario 06: tasks/cancel (cooperative, required)
    //
    // In v2, tasks/cancel is REQUIRED (not optional like v1).
    // ========================================================================
    test('v2-06: tasks/cancel returns cancelled status', async () => {
        const result = await callTool('slow_compute', { seconds: 60, label: 'v2-cancel' });
        const taskId = result.task.taskId;

        const cancelled = await cancelTask(taskId);
        assert.equal(cancelled.status, 'cancelled', 'should be cancelled');

        // Confirm via tasks/get.
        const task = await getTask(taskId);
        assert.equal(task.status, 'cancelled', 'poll after cancel should show cancelled');
    });

    // ========================================================================
    // Scenario 07: tasks/cancel on terminal task returns error
    //
    // Per spec: -32602 (InvalidParams). Enforced from the start since
    // v2 is a new spec.
    // ========================================================================
    test('v2-07: cancel completed task returns error', async () => {
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
    // Scenario 08: tasks/result method does not exist
    //
    // v2 removes tasks/result entirely. Servers MUST reject it.
    // -32601 (MethodNotFound) is mandated by JSON-RPC for unknown methods.
    // ========================================================================
    test('v2-08: tasks/result is rejected (method removed in v2)', async () => {
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
    // Scenario 09: tasks/list method does not exist
    //
    // v2 removes tasks/list entirely.
    // -32601 (MethodNotFound) is mandated by JSON-RPC for unknown methods.
    // ========================================================================
    test('v2-09: tasks/list is rejected (method removed in v2)', async () => {
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
    // Scenario 10: No tasks in initialize capabilities
    //
    // In v2, tasks are core protocol — not negotiated via capabilities.
    // The server SHOULD NOT advertise tasks in initialize.capabilities.
    // ========================================================================
    test('v2-10: tasks not advertised in initialize capabilities', async () => {
        // Re-read the server's capabilities from the initialization response.
        // The client stores these after connect().
        const caps = (client as any)._serverCapabilities ?? (client as any).serverCapabilities;
        if (caps) {
            assert.ok(!caps.tasks,
                'v2 server should not advertise tasks in capabilities (tasks are core protocol)');
        }
        // If we can't access capabilities, skip — this is a structural check.
    });

    // ========================================================================
    // Scenario 11: TTL in seconds (not milliseconds)
    //
    // v2 aligns TTL with SEP-2549: seconds, not milliseconds.
    // A TTL of 300 means 5 minutes, not 0.3 seconds.
    //
    // OPEN QUESTION @LucaButBoring: Is there a programmatic way to distinguish
    // seconds from milliseconds, or is this purely a documentation/convention
    // change? The heuristic below (ttl < 10000) catches common defaults but
    // a server with a very long TTL (e.g., 24 hours = 86400s) would pass
    // either way.
    // ========================================================================
    test('v2-11: TTL is in seconds', async () => {
        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-ttl' });
        const task = result.task;
        assert.ok(task.ttl !== undefined, 'task should have ttl');
        assert.ok(typeof task.ttl === 'number' && task.ttl > 0, 'ttl should be a positive number');
        // Heuristic: typical server defaults are 60-600 seconds. If a server
        // returns TTL > 10000, it's likely still using milliseconds.
        // This is a best-effort check — not a spec requirement.
        assert.ok(task.ttl < 10000,
            `ttl should be in seconds (got ${task.ttl} — if >10000, likely milliseconds)`);
    });

    // ========================================================================
    // Scenario 12: Task not expired before TTL
    //
    // Same as v1 — servers MUST NOT expire before TTL elapses.
    // ========================================================================
    test('v2-12: task must not expire before TTL', async () => {
        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-ttl-guard' });
        const taskId = result.task.taskId;
        await waitForTerminal(client, taskId);

        // Task should still be accessible well before TTL (which is in seconds now).
        await new Promise(r => setTimeout(r, 500));
        const task = await getTask(taskId);
        assert.ok(task.taskId, 'task should still exist before TTL expires');
    });

    // ========================================================================
    // Scenario 13: requestState returned by server
    //
    // v2 adds requestState for stateless deployments. The server MAY return
    // a requestState in tasks/get responses.
    // ========================================================================
    test('v2-13: tasks/get response may include requestState', async () => {
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
        // No assertion on presence — it's optional.
    });

    // ========================================================================
    // Scenario 14: requestState echoed by client
    //
    // If the server returns requestState, the client MUST echo it back
    // in subsequent tasks/get and tasks/cancel requests.
    // ========================================================================
    test('v2-14: client echoes requestState in subsequent requests', async () => {
        const result = await callTool('slow_compute', { seconds: 2, label: 'v2-reqstate-echo' });
        const taskId = result.task.taskId;

        // First poll — may get requestState.
        const first = await getTask(taskId);
        const state = first.requestState;

        if (state) {
            // Echo requestState in next poll.
            const second = await getTask(taskId, { requestState: state });
            assert.ok(second.taskId, 'should still return task info');
            // Server may return a new requestState.
            if (second.requestState !== undefined) {
                assert.equal(typeof second.requestState, 'string',
                    'updated requestState should be a string');
            }
        }
        // If no requestState on first poll, the server doesn't use it — skip.
    });

    // ========================================================================
    // Scenario 15: inputRequests via tasks/get
    //
    // When a task needs input (elicitation/sampling), v2 returns
    // status: input_required with an inputRequests array in tasks/get.
    // This replaces the v1 side-channel via tasks/result.
    //
    // PROVISIONAL: The exact field names (inputRequests vs inputRequest)
    // and delivery mechanism are under active discussion in SEP-2557.
    // There is debate about whether inputResponses should live on tasks/get
    // or a separate tasks/continue method. This test uses the current SEP
    // text and WILL NEED UPDATING if the spec changes.
    //
    // OPEN QUESTION @LucaButBoring: Is tasks/get the right place for
    // inputRequests/inputResponses, or will this move to tasks/continue?
    // ========================================================================
    test('v2-15: input_required task has inputRequests in tasks/get', async () => {
        const result = await callTool('confirm_delete', { filename: 'v2-input.txt' });
        const taskId = result.task.taskId;

        // Wait for input_required.
        const task = await waitForStatus(client, taskId, 'input_required', 5000);
        assert.equal(task.status, 'input_required', 'should be input_required');

        // v2: inputRequests should be inlined in the tasks/get response.
        assert.ok(task.inputRequests, 'input_required task should have inputRequests');
        assert.ok(Array.isArray(task.inputRequests), 'inputRequests should be an array');
        assert.ok(task.inputRequests.length > 0, 'inputRequests should not be empty');

        // Each request should have a method (e.g., elicitation/create).
        const req = task.inputRequests[0];
        assert.ok(req.method || req.type,
            'inputRequest should have a method or type field');
    });

    // ========================================================================
    // Scenario 16: inputResponses via tasks/get
    //
    // Client sends inputResponses in a subsequent tasks/get call to
    // provide the requested input. The task should resume.
    //
    // PROVISIONAL: Same caveat as scenario 15 — the delivery mechanism
    // for inputResponses is under active debate. This test uses tasks/get
    // per the current SEP text.
    // ========================================================================
    test('v2-16: inputResponses resumes task', async () => {
        const result = await callTool('confirm_delete', { filename: 'v2-respond.txt' });
        const taskId = result.task.taskId;

        // Wait for input_required.
        const inputTask = await waitForStatus(client, taskId, 'input_required', 5000);
        assert.equal(inputTask.status, 'input_required', 'should be input_required');

        // Get requestState if available.
        const state = inputTask.requestState;

        // Send inputResponses — confirm the deletion.
        // The exact shape depends on the inputRequest type.
        const responses = [{
            // Elicitation response: accept with content.
            action: 'accept',
            content: { confirm: true }
        }];

        const resumed = await getTask(taskId, {
            requestState: state,
            inputResponses: responses
        });

        // Task should have resumed — either working or completed.
        assert.ok(
            ['working', 'completed'].includes(resumed.status),
            `task should have resumed, got ${resumed.status}`
        );

        // If not yet completed, wait for completion.
        if (resumed.status !== 'completed') {
            const terminal = await waitForTerminal(client, taskId);
            assert.equal(terminal.status, 'completed', 'task should complete after input');
        }
    });

    // ========================================================================
    // Scenario 17: Status notification with DetailedTask (optional)
    //
    // v2 status notifications include the full DetailedTask, so terminal
    // notifications have inlined result/error. Notifications are optional.
    // ========================================================================
    test('v2-17: status notifications include DetailedTask if sent', async () => {
        const statusEvents: any[] = [];

        client.setNotificationHandler('notifications/tasks/status', (notification: any) => {
            statusEvents.push(notification.params);
        });

        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-notify' });
        const taskId = result.task.taskId;
        await waitForTerminal(client, taskId);
        await new Promise(r => setTimeout(r, 500));

        if (statusEvents.length > 0) {
            // If notifications were sent, verify they're well-formed.
            for (const evt of statusEvents) {
                assert.ok(evt.taskId, 'status notification should have taskId');
                assert.ok(evt.status, 'status notification should have status');
            }

            // Terminal notifications should include inlined result (v2 DetailedTask).
            const terminal = statusEvents.filter(
                (e: any) => e.taskId === taskId && ['completed', 'failed'].includes(e.status)
            );
            if (terminal.length > 0) {
                const last = terminal[terminal.length - 1];
                if (last.status === 'completed') {
                    assert.ok(last.result,
                        'v2 completed notification should include inlined result');
                }
            }
        }
        // No assertion on count — notifications are optional.
    });

    // ========================================================================
    // Scenario 18: No client `task` param needed
    //
    // In v2, execution.taskSupport is removed. The server decides whether
    // to create a task. The client just calls tools/call normally.
    // A tool that was "required" in v1 now simply always returns a task.
    // ========================================================================
    test('v2-18: tools/call without task param creates task for async tools', async () => {
        // In v1, failing_job required a `task` param. In v2, it doesn't.
        const result = await callTool('failing_job', {});
        // Server should still create a task for this tool.
        assert.ok(result.task, 'async tool should return CreateTaskResult');
        assert.ok(result.task.taskId, 'task should have taskId');
    });

    // ========================================================================
    // Scenario 19: Immediate result shortcut
    //
    // v2 allows servers to return an immediate result even for task-capable
    // tools when the operation completes fast enough. The server MAY return
    // a CallToolResult (no task) or a CreateTaskResult (with task).
    // Both are valid responses.
    // ========================================================================
    test('v2-19: server may return immediate result for fast operations', async () => {
        // A very fast operation — server may choose to return inline.
        const result = await callTool('slow_compute', { seconds: 0, label: 'v2-instant' });

        // Either a task was created (CreateTaskResult) or result was immediate (CallToolResult).
        if (result.task) {
            // Task path — verify it has taskId.
            assert.ok(result.task.taskId, 'task should have taskId');
        } else {
            // Immediate result path — verify content.
            assert.ok(result.content, 'immediate result should have content');
            assert.ok(Array.isArray(result.content), 'content should be an array');
        }
        // Both paths are valid — this scenario just verifies the server handles it.
    });

    // ========================================================================
    // Scenario 20: related-task _meta in v2 context
    //
    // With tasks/result removed in v2, the related-task metadata question
    // changes. When tasks/get returns an inlined result for a completed task,
    // the taskId is already in the response — so related-task _meta may be
    // redundant. This scenario documents the open question.
    //
    // OPEN QUESTION @LucaButBoring: Does the inlined result in tasks/get need
    // io.modelcontextprotocol/related-task in _meta? The taskId is already
    // at the root of the response. If not needed, this scenario can verify
    // its absence instead.
    // ========================================================================
    test('v2-20: related-task _meta handling in tasks/get inlined results', async () => {
        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-meta' });
        const taskId = result.task.taskId;

        const terminal = await waitForTerminal(client, taskId);
        assert.equal(terminal.status, 'completed', 'should be completed');
        assert.ok(terminal.result, 'should have inlined result');

        // The taskId is already at the root level of the tasks/get response.
        // related-task _meta may be unnecessary/redundant in v2.
        // For now, just verify the result is well-formed.
        assert.ok(terminal.result.content, 'inlined result should have content');
    });

});
