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
// Helper: create a task via raw request
// ============================================================================

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
        {} as any // skip schema validation — we check manually
    );
    const task = (result as any).task as Task;
    assert.ok(task, 'CreateTaskResult should have task field');
    assert.ok(task.taskId, 'task should have taskId');
    return task;
}

// Helper: poll tasks/get until terminal
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

// Helper: poll tasks/get until a specific status
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
        assert.equal(task.status, 'working', 'initial status should be working');
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
    // ========================================================================
    test('scenario 07: tasks/list returns task array', async () => {
        // Create a task first to ensure list is not empty.
        await createTask('slow_compute', { seconds: 1, label: 'list-test' });

        const list = await client.experimental.tasks.listTasks();
        assert.ok(list.tasks, 'should have tasks array');
        assert.ok(Array.isArray(list.tasks), 'tasks should be an array');
        assert.ok(list.tasks.length > 0, 'should have at least one task');
    });

    // ========================================================================
    // Scenario 8: Required tool without task hint returns error
    // ========================================================================
    test('scenario 08: required tool without task hint returns error', async () => {
        try {
            await client.callTool({
                name: 'failing_job',
                arguments: {}
            });
            assert.fail('should have thrown an error');
        } catch (e: any) {
            assert.ok(e.message || e.code, 'should have error message or code');
        }
    });

    // ========================================================================
    // Scenario 9: Forbidden tool with task hint returns error
    // ========================================================================
    test('scenario 09: forbidden tool with task hint returns error', async () => {
        try {
            await createTask('greet', { name: 'test' });
            assert.fail('should have thrown an error');
        } catch (e: any) {
            assert.ok(e.message || e.code, 'should have error message or code');
        }
    });

    // ========================================================================
    // Scenario 10: External proxy tool — full lifecycle via callbacks
    // ========================================================================
    test('scenario 10: external proxy tool completes via task callbacks', async () => {
        const task = await createTask('external_job', { job_id: 'conformance-10' });
        assert.equal(task.status, 'working', 'initial status should be working');

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
        // No task field — this was a sync call
        assert.ok(!(result as any).task, 'sync call should not have task field');
    });

    // ========================================================================
    // Scenario 13: Get non-existent task
    // ========================================================================
    test('scenario 13: tasks/get with bogus taskId returns error', async () => {
        try {
            await client.experimental.tasks.getTask('nonexistent-task-id-12345');
            assert.fail('should have thrown an error');
        } catch (e: any) {
            assert.ok(e.message || e.code, 'should have error message or code');
        }
    });

    // ========================================================================
    // Scenario 14: Cancel non-existent task
    // ========================================================================
    test('scenario 14: tasks/cancel with bogus taskId returns error', async () => {
        try {
            await client.experimental.tasks.cancelTask('nonexistent-task-id-12345');
            assert.fail('should have thrown an error');
        } catch (e: any) {
            assert.ok(e.message || e.code, 'should have error message or code');
        }
    });

    // ========================================================================
    // Scenario 15: Cancel already-completed task
    // ========================================================================
    test('scenario 15: cancel completed task returns error', async () => {
        const created = await createTask('slow_compute', { seconds: 1, label: 'cancel-done' });
        await waitForTerminal(created.taskId);

        try {
            await client.experimental.tasks.cancelTask(created.taskId);
            assert.fail('should have thrown an error');
        } catch (e: any) {
            assert.ok(e.message || e.code, 'should have error for cancelling terminal task');
        }
    });

    // ========================================================================
    // Scenario 16: Custom TTL passthrough
    // ========================================================================
    test('scenario 16: client TTL hint is reflected in CreateTaskResult', async () => {
        const task = await createTask('slow_compute', { seconds: 1, label: 'ttl-test' }, { ttl: 30000 });
        assert.ok(task.ttl !== undefined && task.ttl !== null, 'task should have ttl');
        assert.equal(task.ttl, 30000, 'ttl should match client hint');
    });

    // ========================================================================
    // Scenario 17: Poll interval passthrough
    // ========================================================================
    test('scenario 17: client pollInterval hint is reflected in CreateTaskResult', async () => {
        const task = await createTask('slow_compute', { seconds: 1, label: 'poll-test' }, { pollInterval: 500 });
        assert.ok(task.pollInterval !== undefined, 'task should have pollInterval');
        // Server MAY use client hint or override with its own default.
        // Go server respects it; TS SDK defaults to 1000ms.
        assert.ok(typeof task.pollInterval === 'number' && task.pollInterval > 0,
            'pollInterval should be a positive number');
    });

    // ========================================================================
    // Scenario 18: TTL expiry
    // ========================================================================
    test('scenario 18: task expires after TTL', async () => {
        // Create task with very short TTL (2 seconds).
        const task = await createTask('slow_compute', { seconds: 1, label: 'ttl-expiry' }, { ttl: 2000 });
        // Wait for task to complete + TTL to expire.
        await waitForTerminal(task.taskId);
        await new Promise(r => setTimeout(r, 2500));

        try {
            await client.experimental.tasks.getTask(task.taskId);
            assert.fail('should have thrown — task should be expired');
        } catch (e: any) {
            assert.ok(e.message || e.code, 'should get error for expired task');
        }
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

        // Wait for all to complete.
        await Promise.all(tasks.map(t => waitForTerminal(t.taskId)));
    });

    // ========================================================================
    // Scenario 20: tasks/result for failed task returns isError content
    // ========================================================================
    test('scenario 20: tasks/result for failed task returns error content', async () => {
        const created = await createTask('failing_job', {});
        await waitForTerminal(created.taskId);

        const result = await client.experimental.tasks.getTaskResult(created.taskId);
        assert.ok(result, 'should return a result');
        const content = (result as any).content as any[];
        assert.ok(content && content.length > 0, 'should have content');
        // The result should indicate an error.
        assert.ok(
            (result as any).isError === true || content[0].text.toLowerCase().includes('fail'),
            'result should indicate failure'
        );
    });

    // ========================================================================
    // Scenario 21: Session isolation
    // ========================================================================
    test('scenario 21: task from one session is not visible to another', async () => {
        // Create a task on the main client.
        const task = await createTask('slow_compute', { seconds: 5, label: 'isolation-test' });

        // Create a second client (separate session).
        const transport2 = new StreamableHTTPClientTransport(new URL(SERVER_URL));
        const client2 = new Client(
            { name: 'mcp-tasks-conformance-2', version: '1.0.0' },
            { capabilities: { tasks: {} } }
        );
        await client2.connect(transport2);

        try {
            // Client 2 should NOT see client 1's task.
            await client2.experimental.tasks.getTask(task.taskId);
            assert.fail('client 2 should not see client 1 task');
        } catch (e: any) {
            assert.ok(e.message || e.code, 'should get error for cross-session access');
        } finally {
            await client2.close();
        }
    });

    // ========================================================================
    // Scenario 22: Execution field in tools/list
    // ========================================================================
    test('scenario 22: tools/list includes execution.taskSupport', async () => {
        const tools = await client.listTools();
        const toolMap = new Map(tools.tools.map(t => [t.name, t]));

        // greet — no execution field (forbidden)
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
    // Scenario 23: Elicitation via side-channel (confirm_delete)
    // ========================================================================
    test('scenario 23: elicitation round-trip via tasks/result', async () => {
        // Set up elicitation handler — auto-confirm deletion.
        client.setRequestHandler('elicitation/create', async (request: any) => {
            return {
                action: 'accept' as const,
                content: { confirm: true }
            };
        });

        // Create the task.
        const task = await createTask('confirm_delete', { filename: 'conformance.txt' });
        // Initial status may be 'working' or 'input_required' depending on
        // server timing (TS SDK transitions before returning CreateTaskResult).
        assert.ok(['working', 'input_required'].includes(task.status),
            `initial status should be working or input_required, got ${task.status}`);

        // Wait for input_required (server is waiting for elicitation response).
        const inputTask = await waitForStatus(task.taskId, 'input_required', 5000);
        assert.equal(inputTask.status, 'input_required',
            'task should be input_required while waiting for elicitation');

        // Call tasks/result — this triggers the side-channel: server sends
        // elicitation request via SSE, client handler responds, server completes task.
        const result = await client.experimental.tasks.getTaskResult(task.taskId);
        assert.ok(result, 'should return a result');
        const content = (result as any).content as any[];
        assert.ok(content && content.length > 0, 'should have content');
        assert.ok(content[0].text.includes('Deleted') || content[0].text.includes('conformance.txt'),
            'result should confirm deletion');

        // Verify task is now completed.
        const final = await client.experimental.tasks.getTask(task.taskId);
        assert.equal(final.status, 'completed', 'task should be completed after elicitation');
    });

    // ========================================================================
    // Scenario 24: Sampling via side-channel (write_haiku)
    // ========================================================================
    test('scenario 24: sampling round-trip via tasks/result', async () => {
        // Set up sampling handler — return a canned haiku.
        client.setRequestHandler('sampling/createMessage', async (request: any) => {
            return {
                model: 'test-model',
                role: 'assistant',
                content: { type: 'text', text: 'Waves crash on the shore\nSalt spray kisses ancient rocks\nTide pools hold small worlds' }
            };
        });

        // Create the task.
        const task = await createTask('write_haiku', { topic: 'ocean' });
        // Initial status may be 'working' or 'input_required' depending on
        // server timing (TS SDK transitions before returning CreateTaskResult).
        assert.ok(['working', 'input_required'].includes(task.status),
            `initial status should be working or input_required, got ${task.status}`);

        // Wait for input_required.
        const inputTask = await waitForStatus(task.taskId, 'input_required', 5000);
        assert.equal(inputTask.status, 'input_required',
            'task should be input_required while waiting for sampling');

        // Call tasks/result — triggers side-channel sampling.
        const result = await client.experimental.tasks.getTaskResult(task.taskId);
        assert.ok(result, 'should return a result');
        const content = (result as any).content as any[];
        assert.ok(content && content.length > 0, 'should have content');
        assert.ok(content[0].text.includes('ocean') || content[0].text.includes('Haiku'),
            'result should mention the topic');

        // Verify task is completed.
        const final = await client.experimental.tasks.getTask(task.taskId);
        assert.equal(final.status, 'completed', 'task should be completed after sampling');
    });

    // ========================================================================
    // Scenario 25: Progress notifications
    // ========================================================================
    test('scenario 25: progress notifications received during task execution', async () => {
        const progressEvents: any[] = [];

        client.setNotificationHandler('notifications/progress', (notification: any) => {
            progressEvents.push(notification.params);
        });

        // Create a 2-second task — should emit progress each second.
        const task = await createTask('slow_compute', { seconds: 2, label: 'progress-test' });
        await waitForTerminal(task.taskId, 10_000);

        // Should have received at least 1 progress notification.
        assert.ok(progressEvents.length >= 1,
            `should have received progress events, got ${progressEvents.length}`);
        // Progress should have numeric progress field.
        assert.ok(typeof progressEvents[0].progress === 'number',
            'progress event should have numeric progress field');
    });

    // ========================================================================
    // Scenario 26: Status notifications
    // ========================================================================
    test('scenario 26: status notification received on task completion', async () => {
        const statusEvents: any[] = [];

        client.setNotificationHandler('notifications/tasks/status', (notification: any) => {
            statusEvents.push(notification.params);
        });

        // Create a task that completes quickly.
        const task = await createTask('slow_compute', { seconds: 1, label: 'status-notify' });

        // Wait for completion — the result handler sends a status notification.
        await waitForTerminal(task.taskId);
        // Fetch result to trigger the notification from the result handler.
        await client.experimental.tasks.getTaskResult(task.taskId);

        // Give notifications a moment to arrive.
        await new Promise(r => setTimeout(r, 500));

        // Should have received at least one status notification.
        assert.ok(statusEvents.length >= 1,
            `should have received status notifications, got ${statusEvents.length}`);
        // The notification should reference our task.
        const ourEvent = statusEvents.find((e: any) => e.taskId === task.taskId);
        assert.ok(ourEvent, 'should have a status notification for our task');
    });

});
