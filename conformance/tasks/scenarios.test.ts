/**
 * MCP Tasks Conformance Scenarios
 *
 * Tests any MCP server that implements the Tasks protocol (spec 2025-11-25).
 * Uses the official MCP TypeScript SDK client — if these scenarios pass,
 * the server is conformant.
 *
 * Usage:
 *   cd conformance && npm install
 *   SERVER_URL=http://localhost:8080/mcp npx tsx --test tasks/scenarios.test.ts
 *
 * The server MUST register these tools:
 *   - greet (no task support) — sync, returns "Hello, {name}!"
 *   - slow_compute (optional task support) — sleeps N seconds
 *   - failing_job (required task support) — fails after 1s
 *   - external_job (required task support) — completes after 1s, uses TaskCallbacks
 *   - confirm_delete (required task support) — elicitation: asks user before deleting
 *   - write_haiku (required task support) — sampling: asks LLM to write a haiku
 */

import { describe, test, before, after } from 'node:test';
import { strict as assert } from 'node:assert';
import { Client, StreamableHTTPClientTransport } from '@modelcontextprotocol/client';
import type { Task, CallToolResult, CreateTaskResult } from '@modelcontextprotocol/core';

const SERVER_URL = process.env.SERVER_URL || 'http://localhost:8080/mcp';

let client: Client;

before(async () => {
    const transport = new StreamableHTTPClientTransport(new URL(SERVER_URL));
    client = new Client(
        { name: 'mcp-tasks-conformance', version: '1.0.0' },
        { capabilities: { tasks: {}, elicitation: {}, sampling: {} } }
    );
    await client.connect(transport);
});

after(async () => {
    await client.close();
});

// ============================================================================
// Helpers
// ============================================================================

/** Create a task via raw request (bypasses SDK schema validation). */
async function createTask(toolName: string, args: Record<string, unknown>, taskOpts: Record<string, unknown> = {}): Promise<Task> {
    const result = await client.request(
        {
            method: 'tools/call',
            params: {
                name: toolName,
                arguments: args,
                task: taskOpts,
            },
        },
        {} as any
    );
    const task = (result as any).task as Task;
    assert.ok(task, 'CreateTaskResult should have task field');
    assert.ok(task.taskId, 'task should have taskId');
    return task;
}

/** Poll tasks/get until a terminal state. */
async function waitForTerminal(taskId: string, timeoutMs = 10_000): Promise<Task> {
    const start = Date.now();
    while (Date.now() - start < timeoutMs) {
        const task = await client.experimental.tasks.getTask(taskId);
        if (['completed', 'failed', 'cancelled'].includes(task.status)) {
            return task;
        }
        await new Promise(r => setTimeout(r, 200));
    }
    throw new Error(`Task ${taskId} did not reach terminal state within ${timeoutMs}ms`);
}

/** Poll tasks/get until a specific status or terminal. */
async function waitForStatus(taskId: string, status: string, timeoutMs = 10_000): Promise<Task> {
    const start = Date.now();
    while (Date.now() - start < timeoutMs) {
        const task = await client.experimental.tasks.getTask(taskId);
        if (task.status === status || ['completed', 'failed', 'cancelled'].includes(task.status)) {
            return task;
        }
        await new Promise(r => setTimeout(r, 200));
    }
    throw new Error(`Task ${taskId} did not reach status ${status} within ${timeoutMs}ms`);
}

// TODO: Set to true once the spec mandates specific error codes for task
// operations and TS SDK enforces them. When enabled, all error scenarios
// will assert the exact code instead of just "any numeric code."
const ENFORCE_ERROR_CODES = false;

/**
 * Assert a JSON-RPC error on the caught error object.
 *
 * Always verifies the error has a numeric code. When `enforce` is true,
 * also asserts it matches `expectedCode`. Defaults to ENFORCE_ERROR_CODES
 * but can be overridden per-test for cases where a specific code IS
 * mandated by the spec.
 */
function assertJsonRpcError(e: any, expectedCode: number, label: string, enforce = ENFORCE_ERROR_CODES) {
    const code = e.code ?? e.error?.code;
    assert.ok(typeof code === 'number', `${label}: error should have a numeric code, got ${typeof code}`);
    if (enforce) {
        assert.equal(code, expectedCode, `${label}: expected code ${expectedCode}, got ${code}`);
    }
}

// ============================================================================
// Scenarios
// ============================================================================

describe('MCP Tasks Conformance', () => {

    // ========================================================================
    // Scenario 1: Sync tool call (no task support)
    // ========================================================================
    test('scenario 01: sync tool call returns immediately', async () => {
        const result = await client.callTool({
            name: 'greet',
            arguments: { name: 'World' }
        });
        const content = result.content as any[];
        assert.ok(content.length > 0, 'should have content');
        assert.equal(content[0].type, 'text');
        assert.equal(content[0].text, 'Hello, World!');
    });

    // ========================================================================
    // Scenario 2: Async task creation
    // ========================================================================
    test('scenario 02: async task creation returns CreateTaskResult', async () => {
        const task = await createTask('slow_compute', { seconds: 2, label: 'conformance' });
        // A fast server could transition beyond 'working' before the response
        // flushes, so accept any non-terminal status.
        assert.ok(
            !['completed', 'failed', 'cancelled'].includes(task.status),
            `initial status should be non-terminal, got ${task.status}`
        );
        assert.ok(task.createdAt, 'task should have createdAt');
        assert.ok(task.lastUpdatedAt, 'task should have lastUpdatedAt');
    });

    // ========================================================================
    // Scenario 3: Poll task status via tasks/get
    // ========================================================================
    test('scenario 03: tasks/get returns flat task info', async () => {
        const created = await createTask('slow_compute', { seconds: 1, label: 'poll-test' });

        const task = await client.experimental.tasks.getTask(created.taskId);
        assert.ok(task.taskId, 'should have taskId at root level');
        assert.ok(task.status, 'should have status at root level');
    });

    // ========================================================================
    // Scenario 4: Failing job transitions to failed
    // ========================================================================
    test('scenario 04: failing tool transitions to failed', async () => {
        const created = await createTask('failing_job', {});
        const terminal = await waitForTerminal(created.taskId);
        assert.equal(terminal.status, 'failed', 'task should be failed');
    });

    // ========================================================================
    // Scenario 5: Task completion and result retrieval
    // ========================================================================
    test('scenario 05: tasks/result returns ToolResult with related-task meta', async () => {
        const created = await createTask('slow_compute', { seconds: 1, label: 'result-test' });
        await waitForTerminal(created.taskId);

        const result = await client.experimental.tasks.getTaskResult(created.taskId);
        assert.ok(result, 'should return a result');
        const meta = (result as any)._meta;
        assert.ok(meta, 'result should have _meta');
        const related = meta['io.modelcontextprotocol/related-task'];
        assert.ok(related, 'should have related-task in _meta');
        assert.equal(related.taskId, created.taskId, 'related taskId should match');
    });

    // ========================================================================
    // Scenario 6: Task cancellation
    // ========================================================================
    test('scenario 06: cancel returns cancelled status', async () => {
        const created = await createTask('slow_compute', { seconds: 60, label: 'cancel-test' });

        const cancelled = await client.experimental.tasks.cancelTask(created.taskId);
        assert.equal(cancelled.status, 'cancelled', 'should be cancelled');

        const task = await client.experimental.tasks.getTask(created.taskId);
        assert.equal(task.status, 'cancelled', 'poll after cancel should show cancelled');
    });

    // ========================================================================
    // Scenario 7: tasks/list returns array
    //
    // Note: tasks/list is capability-conditional (tasks.list). This test
    // assumes the server advertises it. Servers that don't are not required
    // to implement it.
    // ========================================================================
    test('scenario 07: tasks/list returns task array', async () => {
        await createTask('slow_compute', { seconds: 1, label: 'list-test' });

        const list = await client.experimental.tasks.listTasks();
        assert.ok(list.tasks, 'should have tasks array');
        assert.ok(Array.isArray(list.tasks), 'tasks should be an array');
        assert.ok(list.tasks.length > 0, 'should have at least one task');
    });

    // ========================================================================
    // Scenario 8: Required tool without task hint returns error
    //
    // Calling a tool with execution.taskSupport=required without a task hint
    // MUST return -32601 (MethodNotFound) per spec. The TS SDK currently
    // returns -32603 which is incorrect. ENFORCE_ERROR_CODES is off by
    // default to avoid breaking existing servers, but the expected code
    // is documented and will be enforced once the TS SDK is fixed.
    // ========================================================================
    test('scenario 08: required tool without task hint returns error', async () => {
        try {
            await client.callTool({
                name: 'failing_job',
                arguments: {}
            });
            assert.fail('should have thrown an error');
        } catch (e: any) {
            assertJsonRpcError(e, -32601, 'required without hint');
        }
    });

    // ========================================================================
    // Scenario 9: Forbidden tool with task hint returns error
    //
    // Sending a task hint to a tool that does not support tasks (absent or
    // forbidden execution) MUST return -32601 (MethodNotFound) per spec.
    // TS SDK currently returns -32603 (incorrect). Same ENFORCE_ERROR_CODES
    // gating as scenario 8.
    // ========================================================================
    test('scenario 09: forbidden tool with task hint returns error', async () => {
        try {
            await createTask('greet', { name: 'test' });
            assert.fail('should have thrown an error');
        } catch (e: any) {
            assertJsonRpcError(e, -32601, 'forbidden with hint');
        }
    });

    // ========================================================================
    // Scenario 10: External proxy tool — full lifecycle via callbacks
    // ========================================================================
    test('scenario 10: external proxy tool completes via task callbacks', async () => {
        const task = await createTask('external_job', { job_id: 'conformance-10' });
        assert.ok(
            !['completed', 'failed', 'cancelled'].includes(task.status),
            `initial status should be non-terminal, got ${task.status}`
        );

        const terminal = await waitForTerminal(task.taskId);
        assert.equal(terminal.status, 'completed', 'task should complete');

        const result = await client.experimental.tasks.getTaskResult(task.taskId);
        assert.ok(result, 'should return a result');
        const meta = (result as any)._meta;
        assert.ok(meta, 'result should have _meta');
        const related = meta['io.modelcontextprotocol/related-task'];
        assert.ok(related, 'should have related-task in _meta');
        assert.equal(related.taskId, task.taskId, 'related taskId should match');
    });

    // ========================================================================
    // Scenario 11: External proxy tool — tasks/get returns correct state
    // ========================================================================
    test('scenario 11: external proxy tool tasks/get returns task info', async () => {
        const task = await createTask('external_job', { job_id: 'conformance-11' });

        const info = await client.experimental.tasks.getTask(task.taskId);
        assert.ok(info.taskId, 'should have taskId');
        assert.ok(info.status, 'should have status');
        assert.ok(
            ['working', 'completed'].includes(info.status),
            `status should be working or completed, got ${info.status}`
        );
    });

    // ========================================================================
    // Scenario 12: Optional tool sync (no task hint)
    // ========================================================================
    test('scenario 12: optional tool without hint runs synchronously', async () => {
        const result = await client.callTool({
            name: 'slow_compute',
            arguments: { seconds: 1, label: 'sync-test' }
        });
        const content = result.content as any[];
        assert.ok(content.length > 0, 'should have content');
        assert.equal(content[0].type, 'text');
        assert.ok(content[0].text.includes('sync-test'), 'result should mention label');
        // No task field — this was a sync call.
        assert.ok(!(result as any).task, 'sync call should not have task field');
    });

    // ========================================================================
    // Scenario 13: Get non-existent task returns error
    //
    // Per spec, task-not-found MUST return -32602 (InvalidParams).
    // TS SDK currently returns -32603 (incorrect). ENFORCE_ERROR_CODES
    // is off by default to avoid breaking existing servers.
    // ========================================================================
    test('scenario 13: tasks/get with bogus taskId returns error', async () => {
        try {
            await client.experimental.tasks.getTask('nonexistent-task-id-12345');
            assert.fail('should have thrown an error');
        } catch (e: any) {
            assertJsonRpcError(e, -32602, 'get non-existent');
        }
    });

    // ========================================================================
    // Scenario 14: Cancel non-existent task returns error
    //
    // Per spec: -32602 (InvalidParams). Same as scenario 13.
    // ========================================================================
    test('scenario 14: tasks/cancel with bogus taskId returns error', async () => {
        try {
            await client.experimental.tasks.cancelTask('nonexistent-task-id-12345');
            assert.fail('should have thrown an error');
        } catch (e: any) {
            assertJsonRpcError(e, -32602, 'cancel non-existent');
        }
    });

    // ========================================================================
    // Scenario 15: Cancel already-completed task returns error
    //
    // Per spec: -32602 (InvalidParams). Same as scenario 13.
    // ========================================================================
    test('scenario 15: cancel completed task returns error', async () => {
        const created = await createTask('slow_compute', { seconds: 1, label: 'cancel-done' });
        await waitForTerminal(created.taskId);

        try {
            await client.experimental.tasks.cancelTask(created.taskId);
            assert.fail('should have thrown an error');
        } catch (e: any) {
            assertJsonRpcError(e, -32602, 'cancel completed');
        }
    });

    // ========================================================================
    // Scenario 16: TTL in CreateTaskResult
    //
    // The client's task.ttl is a statement of intent — the server MAY use a
    // different value. We only verify the response includes a TTL and that
    // it's a positive number.
    // ========================================================================
    test('scenario 16: CreateTaskResult includes a TTL', async () => {
        const task = await createTask('slow_compute', { seconds: 1, label: 'ttl-test' }, { ttl: 30000 });
        assert.ok(task.ttl !== undefined && task.ttl !== null, 'task should have ttl');
        assert.ok(typeof task.ttl === 'number' && task.ttl > 0, 'ttl should be a positive number');
    });

    // ========================================================================
    // Scenario 17: pollInterval in CreateTaskResult
    //
    // pollInterval is an optional server-provided field telling the client
    // how often to poll. It is NOT a client request parameter (that was a
    // TS SDK bug). If present, it should be a positive number.
    // ========================================================================
    test('scenario 17: CreateTaskResult pollInterval is valid if present', async () => {
        const task = await createTask('slow_compute', { seconds: 1, label: 'poll-test' });
        // pollInterval is optional per spec.
        if (task.pollInterval !== undefined) {
            assert.ok(typeof task.pollInterval === 'number' && task.pollInterval > 0,
                'pollInterval should be a positive number if present');
        }
    });

    // ========================================================================
    // Scenario 18: TTL — task must not expire before TTL
    //
    // Per spec, servers MUST NOT expire a task before the TTL elapses.
    // Servers MAY expire at any point after — we don't require immediate
    // expiry post-TTL.
    // ========================================================================
    test('scenario 18: task must not expire before TTL', async () => {
        // Create with a generous TTL (5 seconds).
        const task = await createTask('slow_compute', { seconds: 1, label: 'ttl-guard' }, { ttl: 5000 });
        await waitForTerminal(task.taskId);

        // Task MUST still be accessible well before TTL expires.
        // (Task completed after ~1s, TTL is 5s, so at ~1.5s it should exist.)
        await new Promise(r => setTimeout(r, 500));
        const info = await client.experimental.tasks.getTask(task.taskId);
        assert.ok(info.taskId, 'task should still exist before TTL expires');
        assert.equal(info.status, 'completed', 'task should be completed');
    });

    // ========================================================================
    // Scenario 19: Concurrent task creation
    // ========================================================================
    test('scenario 19: concurrent task creation produces unique taskIds', async () => {
        const tasks = await Promise.all(
            Array.from({ length: 5 }, (_, i) =>
                createTask('slow_compute', { seconds: 1, label: `concurrent-${i}` })
            )
        );
        const ids = new Set(tasks.map(t => t.taskId));
        assert.equal(ids.size, 5, 'all 5 tasks should have unique IDs');

        await Promise.all(tasks.map(t => waitForTerminal(t.taskId)));
    });

    // ========================================================================
    // Scenario 20: tasks/result for failed task returns isError
    //
    // Per spec: a failed task's result MUST have isError: true.
    // ========================================================================
    test('scenario 20: tasks/result for failed task has isError true', async () => {
        const created = await createTask('failing_job', {});
        await waitForTerminal(created.taskId);

        const result = await client.experimental.tasks.getTaskResult(created.taskId);
        assert.ok(result, 'should return a result');
        assert.equal((result as any).isError, true, 'result should have isError: true');
    });

    // ========================================================================
    // Scenario 21: Execution field in tools/list
    // ========================================================================
    test('scenario 21: tools/list includes execution.taskSupport', async () => {
        const tools = await client.listTools();
        const toolMap = new Map(tools.tools.map(t => [t.name, t]));

        // greet — no execution field (forbidden per spec: absent = forbidden)
        const greet = toolMap.get('greet');
        assert.ok(greet, 'greet tool should exist');
        assert.ok(!greet.execution || greet.execution.taskSupport === 'forbidden',
            'greet should have no execution or forbidden');

        // slow_compute — optional
        const slow = toolMap.get('slow_compute');
        assert.ok(slow, 'slow_compute tool should exist');
        assert.ok(slow.execution, 'slow_compute should have execution');
        assert.equal((slow.execution as any).taskSupport, 'optional',
            'slow_compute should be optional');

        // failing_job — required
        const fail = toolMap.get('failing_job');
        assert.ok(fail, 'failing_job tool should exist');
        assert.ok(fail.execution, 'failing_job should have execution');
        assert.equal((fail.execution as any).taskSupport, 'required',
            'failing_job should be required');
    });

    // ========================================================================
    // Scenario 22: Elicitation via side-channel (confirm_delete)
    // ========================================================================
    test('scenario 22: elicitation round-trip via tasks/result', async () => {
        client.setRequestHandler('elicitation/create', async (request: any) => {
            return {
                action: 'accept' as const,
                content: { confirm: true }
            };
        });

        const task = await createTask('confirm_delete', { filename: 'conformance.txt' });
        // Initial status may be 'working' or 'input_required' depending on
        // server timing — both are valid.
        assert.ok(['working', 'input_required'].includes(task.status),
            `initial status should be working or input_required, got ${task.status}`);

        const inputTask = await waitForStatus(task.taskId, 'input_required', 5000);
        assert.equal(inputTask.status, 'input_required',
            'task should be input_required while waiting for elicitation');

        // tasks/result triggers the side-channel: server sends elicitation
        // request via SSE, client handler responds, server completes task.
        const result = await client.experimental.tasks.getTaskResult(task.taskId);
        assert.ok(result, 'should return a result');
        const content = (result as any).content as any[];
        assert.ok(content && content.length > 0, 'should have content');
        assert.ok(content[0].text.includes('Deleted') || content[0].text.includes('conformance.txt'),
            'result should confirm deletion');

        const final = await client.experimental.tasks.getTask(task.taskId);
        assert.equal(final.status, 'completed', 'task should be completed after elicitation');
    });

    // ========================================================================
    // Scenario 23: Sampling via side-channel (write_haiku)
    // ========================================================================
    test('scenario 23: sampling round-trip via tasks/result', async () => {
        client.setRequestHandler('sampling/createMessage', async (request: any) => {
            return {
                model: 'test-model',
                role: 'assistant',
                content: { type: 'text', text: 'Waves crash on the shore\nSalt spray kisses ancient rocks\nTide pools hold small worlds' }
            };
        });

        const task = await createTask('write_haiku', { topic: 'ocean' });
        assert.ok(['working', 'input_required'].includes(task.status),
            `initial status should be working or input_required, got ${task.status}`);

        const inputTask = await waitForStatus(task.taskId, 'input_required', 5000);
        assert.equal(inputTask.status, 'input_required',
            'task should be input_required while waiting for sampling');

        const result = await client.experimental.tasks.getTaskResult(task.taskId);
        assert.ok(result, 'should return a result');
        const content = (result as any).content as any[];
        assert.ok(content && content.length > 0, 'should have content');
        assert.ok(content[0].text.includes('ocean') || content[0].text.includes('Haiku'),
            'result should mention the topic');

        const final = await client.experimental.tasks.getTask(task.taskId);
        assert.equal(final.status, 'completed', 'task should be completed after sampling');
    });

    // ========================================================================
    // Scenario 24: Progress notifications (optional)
    //
    // Progress notifications are optional per spec. If the server sends them,
    // they must be well-formed (numeric progress field). We do NOT require
    // that the server sends them.
    // ========================================================================
    test('scenario 24: progress notifications are well-formed if sent', async () => {
        const progressEvents: any[] = [];

        client.setNotificationHandler('notifications/progress', (notification: any) => {
            progressEvents.push(notification.params);
        });

        const task = await createTask('slow_compute', { seconds: 2, label: 'progress-test' });
        await waitForTerminal(task.taskId, 10_000);

        if (progressEvents.length > 0) {
            // If notifications were sent, verify they're well-formed.
            for (const evt of progressEvents) {
                assert.ok(typeof evt.progress === 'number',
                    'progress event should have numeric progress field');
                assert.ok(evt.progressToken !== undefined,
                    'progress event should have progressToken');
            }
        }
        // No assertion on count — notifications are optional.
    });

    // ========================================================================
    // Scenario 25: Status notifications (optional, well-formed if sent)
    //
    // Status notifications are optional per spec. If the server sends them,
    // they must reference the correct task and match the actual task state
    // at the time they were sent.
    // ========================================================================
    test('scenario 25: status notifications match task state if sent', async () => {
        const statusEvents: any[] = [];

        client.setNotificationHandler('notifications/tasks/status', (notification: any) => {
            statusEvents.push(notification.params);
        });

        const task = await createTask('slow_compute', { seconds: 1, label: 'status-notify' });
        await waitForTerminal(task.taskId);
        // Fetch result to trigger any final notification.
        await client.experimental.tasks.getTaskResult(task.taskId);
        await new Promise(r => setTimeout(r, 500));

        if (statusEvents.length > 0) {
            // If notifications were sent, verify they're well-formed.
            for (const evt of statusEvents) {
                assert.ok(evt.taskId, 'status notification should have taskId');
                assert.ok(evt.status, 'status notification should have status');
                assert.ok(
                    ['working', 'completed', 'failed', 'cancelled', 'input_required'].includes(evt.status),
                    `status notification has valid status: ${evt.status}`
                );
            }
            // If any reference our task, they must have a valid status.
            const ours = statusEvents.filter((e: any) => e.taskId === task.taskId);
            if (ours.length > 0) {
                const last = ours[ours.length - 1];
                assert.equal(last.status, 'completed',
                    'final status notification for our task should be completed');
            }
        }
        // No assertion on count — notifications are optional.
    });

    // ========================================================================
    // Scenario 26: related-task _meta on tasks/result
    //
    // Per spec: tasks/result responses MUST include
    // _meta["io.modelcontextprotocol/related-task"] with the taskId.
    // ========================================================================
    test('scenario 26: tasks/result includes related-task _meta', async () => {
        const task = await createTask('slow_compute', { seconds: 1, label: 'meta-test' });
        await waitForTerminal(task.taskId);

        const result = await client.experimental.tasks.getTaskResult(task.taskId);
        const meta = (result as any)._meta;
        assert.ok(meta, 'tasks/result should have _meta');
        const related = meta['io.modelcontextprotocol/related-task'];
        assert.ok(related, 'should have io.modelcontextprotocol/related-task key');
        assert.ok(related.taskId, 'related-task should have taskId');
        assert.equal(related.taskId, task.taskId, 'related-task taskId should match');
    });

    // ========================================================================
    // Scenario 27: tasks/get SHALL NOT include related-task _meta
    //
    // Per spec: tasks/get responses SHALL NOT include related-task metadata
    // because the taskId parameter is the source of truth.
    // ========================================================================
    test('scenario 27: tasks/get does not include related-task _meta', async () => {
        const task = await createTask('slow_compute', { seconds: 1, label: 'no-meta-test' });
        const info = await client.experimental.tasks.getTask(task.taskId);
        const meta = (info as any)._meta;
        if (meta) {
            const related = meta['io.modelcontextprotocol/related-task'];
            assert.ok(!related,
                'tasks/get SHALL NOT include related-task in _meta');
        }
        // No _meta at all is also valid.
    });

});
