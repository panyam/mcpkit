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
    // Failed tasks inline the error in tasks/get, not via tasks/result.
    // ========================================================================
    test('v2-05: tasks/get returns failed status with inlined error', async () => {
        const result = await callTool('failing_job', {});
        const taskId = result.task.taskId;

        const terminal = await waitForTerminal(client, taskId);
        assert.equal(terminal.status, 'failed', 'should be failed');
        // v2: error is inlined in the tasks/get response.
        // The exact field name is `error` per SEP-2557's FailedTask type.
        assert.ok(terminal.error || terminal.result,
            'failed task should have inlined error or result with isError');
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
    // ========================================================================
    test('v2-07: cancel completed task returns error', async () => {
        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-cancel-done' });
        const taskId = result.task.taskId;
        await waitForTerminal(client, taskId);

        try {
            await cancelTask(taskId);
            assert.fail('should have thrown an error');
        } catch (e: any) {
            // v2 is a new spec — enforce error codes from the start.
            assertJsonRpcError(e, -32602, 'cancel completed', true);
        }
    });

    // ========================================================================
    // Scenario 08: tasks/result method does not exist
    //
    // v2 removes tasks/result entirely. Servers MUST reject it.
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
            // -32601 = MethodNotFound per JSON-RPC spec.
            assertJsonRpcError(e, -32601, 'tasks/result removed', true);
        }
    });

    // ========================================================================
    // Scenario 09: tasks/list method does not exist
    //
    // v2 removes tasks/list entirely.
    // ========================================================================
    test('v2-09: tasks/list is rejected (method removed in v2)', async () => {
        try {
            await client.request(
                { method: 'tasks/list', params: {} },
                {} as any
            );
            assert.fail('should have thrown — tasks/list removed in v2');
        } catch (e: any) {
            // -32601 = MethodNotFound per JSON-RPC spec.
            assertJsonRpcError(e, -32601, 'tasks/list removed', true);
        }
    });

    // ========================================================================
    // Scenario 10: TTL in seconds (not milliseconds)
    //
    // v2 aligns TTL with SEP-2549: seconds, not milliseconds.
    // A TTL of 300 means 5 minutes, not 0.3 seconds.
    // ========================================================================
    test('v2-10: TTL is in seconds', async () => {
        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-ttl' });
        const task = result.task;
        assert.ok(task.ttl !== undefined, 'task should have ttl');
        // In v2, TTL is in seconds. A reasonable default is 60-600 seconds.
        // If TTL were in milliseconds, we'd see values like 60000-600000.
        // Assert TTL < 10000 to catch ms-unit bugs (10000s = ~2.7 hours is generous).
        assert.ok(typeof task.ttl === 'number' && task.ttl > 0, 'ttl should be a positive number');
        assert.ok(task.ttl < 10000,
            `ttl should be in seconds (got ${task.ttl} — if >10000, likely milliseconds)`);
    });

    // ========================================================================
    // Scenario 11: Task not expired before TTL
    //
    // Same as v1 — servers MUST NOT expire before TTL elapses.
    // ========================================================================
    test('v2-11: task must not expire before TTL', async () => {
        const result = await callTool('slow_compute', { seconds: 1, label: 'v2-ttl-guard' });
        const taskId = result.task.taskId;
        await waitForTerminal(client, taskId);

        // Task should still be accessible well before TTL (which is in seconds now).
        await new Promise(r => setTimeout(r, 500));
        const task = await getTask(taskId);
        assert.ok(task.taskId, 'task should still exist before TTL expires');
    });

    // ========================================================================
    // Scenario 12: requestState returned by server
    //
    // v2 adds requestState for stateless deployments. The server MAY return
    // a requestState in tasks/get responses.
    // ========================================================================
    test('v2-12: tasks/get response may include requestState', async () => {
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
    // Scenario 13: requestState echoed by client
    //
    // If the server returns requestState, the client MUST echo it back
    // in subsequent tasks/get and tasks/cancel requests.
    // ========================================================================
    test('v2-13: client echoes requestState in subsequent requests', async () => {
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
    // Scenario 14: inputRequests via tasks/get
    //
    // When a task needs input (elicitation/sampling), v2 returns
    // status: input_required with an inputRequests array in tasks/get.
    // This replaces the v1 side-channel via tasks/result.
    //
    // NOTE: The exact field names (inputRequests vs inputRequest) and
    // delivery mechanism are under active discussion in SEP-2557.
    // This test uses inputRequests per the current SEP text.
    // ========================================================================
    test('v2-14: input_required task has inputRequests in tasks/get', async () => {
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
    // Scenario 15: inputResponses via tasks/get
    //
    // Client sends inputResponses in a subsequent tasks/get call to
    // provide the requested input. The task should resume.
    //
    // NOTE: There is an active debate about whether inputResponses should
    // be on tasks/get or a separate tasks/continue method. This test uses
    // tasks/get per the current SEP text. If the spec moves to
    // tasks/continue, this scenario will need updating.
    // ========================================================================
    test('v2-15: inputResponses resumes task', async () => {
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
    // Scenario 16: Status notification with DetailedTask (optional)
    //
    // v2 status notifications include the full DetailedTask, so terminal
    // notifications have inlined result/error. Notifications are optional.
    // ========================================================================
    test('v2-16: status notifications include DetailedTask if sent', async () => {
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
    // Scenario 17: No client `task` param needed
    //
    // In v2, execution.taskSupport is removed. The server decides whether
    // to create a task. The client just calls tools/call normally.
    // A tool that was "required" in v1 now simply always returns a task.
    // ========================================================================
    test('v2-17: tools/call without task param creates task for async tools', async () => {
        // In v1, failing_job required a `task` param. In v2, it doesn't.
        const result = await callTool('failing_job', {});
        // Server should still create a task for this tool.
        assert.ok(result.task, 'async tool should return CreateTaskResult');
        assert.ok(result.task.taskId, 'task should have taskId');
    });

    // ========================================================================
    // Scenario 18: Immediate result shortcut
    //
    // v2 allows servers to return an immediate result even for task-capable
    // tools when the operation completes fast enough. The server MAY return
    // a CallToolResult (no task) or a CreateTaskResult (with task).
    // Both are valid responses.
    // ========================================================================
    test('v2-18: server may return immediate result for fast operations', async () => {
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

});
