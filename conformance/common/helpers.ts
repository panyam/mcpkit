/**
 * Shared helpers for MCP Tasks conformance suites (v1 and v2).
 *
 * Provides assertion utilities and polling helpers that are common
 * across protocol versions.
 */

import { strict as assert } from 'node:assert';
import type { Client } from '@modelcontextprotocol/client';

// TODO: Set to true once the spec mandates specific error codes for task
// operations and TS SDK enforces them. When enabled, all error scenarios
// will assert the exact code instead of just "any numeric code."
export const ENFORCE_ERROR_CODES = false;

/**
 * Assert a JSON-RPC error on the caught error object.
 *
 * Always verifies the error has a numeric code. When `enforce` is true,
 * also asserts it matches `expectedCode`. Defaults to ENFORCE_ERROR_CODES
 * but can be overridden per-test for cases where a specific code IS
 * mandated by the spec.
 */
export function assertJsonRpcError(e: any, expectedCode: number, label: string, enforce = ENFORCE_ERROR_CODES) {
    const code = e.code ?? e.error?.code;
    assert.ok(typeof code === 'number', `${label}: error should have a numeric code, got ${typeof code}`);
    if (enforce) {
        assert.equal(code, expectedCode, `${label}: expected code ${expectedCode}, got ${code}`);
    }
}

/**
 * Poll tasks/get until the task reaches a terminal state.
 * Works with both v1 and v2 — uses raw client.request to avoid
 * SDK version-specific APIs.
 */
export async function waitForTerminal(client: Client, taskId: string, timeoutMs = 10_000): Promise<any> {
    const start = Date.now();
    while (Date.now() - start < timeoutMs) {
        const result = await client.request(
            { method: 'tasks/get', params: { taskId } },
            {} as any
        );
        const task = result as any;
        if (['completed', 'failed', 'cancelled'].includes(task.status)) {
            return task;
        }
        await new Promise(r => setTimeout(r, 200));
    }
    throw new Error(`Task ${taskId} did not reach terminal state within ${timeoutMs}ms`);
}

/**
 * Poll tasks/get until a specific status or terminal.
 */
export async function waitForStatus(client: Client, taskId: string, status: string, timeoutMs = 10_000): Promise<any> {
    const start = Date.now();
    while (Date.now() - start < timeoutMs) {
        const result = await client.request(
            { method: 'tasks/get', params: { taskId } },
            {} as any
        );
        const task = result as any;
        if (task.status === status || ['completed', 'failed', 'cancelled'].includes(task.status)) {
            return task;
        }
        await new Promise(r => setTimeout(r, 200));
    }
    throw new Error(`Task ${taskId} did not reach status ${status} within ${timeoutMs}ms`);
}
