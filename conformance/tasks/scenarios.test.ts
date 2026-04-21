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
        { capabilities: { tasks: {} } }
    );
    await client.connect(transport);
});

after(async () => {
    await client.close();
});

// ============================================================================
// Helper: create a task via raw request (the SDK's callToolStream auto-adds
// task hints, but we need the CreateTaskResult directly)
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

});
