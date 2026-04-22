#!/usr/bin/env node
/**
 * TS SDK reference server — same 5 tools as the Go tasks example.
 * Imports the TS SDK's Server + transport, registers our tools.
 *
 * Setup:
 *   cd examples/tasks && npm install   # first time only
 *
 * Usage:
 *   node ts-reference-server.mjs                # default port 8090
 *   PORT=8090 node ts-reference-server.mjs      # custom port
 */

import { randomUUID } from 'node:crypto';
import { createServer } from 'node:http';

import { Server, InMemoryTaskStore, isTerminal, RELATED_TASK_META_KEY } from '@modelcontextprotocol/server';
import { NodeStreamableHTTPServerTransport } from '@modelcontextprotocol/node';

const PORT = process.env.PORT ? parseInt(process.env.PORT, 10) : 8090;

const taskStore = new InMemoryTaskStore();
const transports = {};

// Per-task AbortController for cancellation propagation (Phase 5 parity).
const taskAbortControllers = new Map();

// ============================================================================
// Tool definitions — mirrors examples/tasks/main.go exactly
// ============================================================================

const TOOLS = [
    {
        name: 'greet',
        description: 'Greet someone (sync-only, no task support)',
        inputSchema: {
            type: 'object',
            properties: { name: { type: 'string', description: 'Name to greet' } },
            required: ['name']
        }
        // No execution = forbidden per spec
    },
    {
        name: 'slow_compute',
        description: 'Simulate a slow computation (sleeps for the given duration). Supports optional async task execution.',
        inputSchema: {
            type: 'object',
            properties: {
                seconds: { type: 'integer', description: 'How many seconds to compute (sleep)', default: 3 },
                label: { type: 'string', description: 'A label for the computation', default: 'default' }
            }
        },
        execution: { taskSupport: 'optional' }
    },
    {
        name: 'failing_job',
        description: 'A job that always fails after 1 second. Requires task invocation.',
        inputSchema: { type: 'object', properties: {} },
        execution: { taskSupport: 'required' }
    },
    {
        name: 'confirm_delete',
        description: 'Asks for confirmation before deleting a file. Demonstrates task-based elicitation.',
        inputSchema: {
            type: 'object',
            properties: { filename: { type: 'string', description: 'File to delete', default: 'important.txt' } }
        },
        execution: { taskSupport: 'required' }
    },
    {
        name: 'write_haiku',
        description: 'Asks the LLM to write a haiku on a topic. Demonstrates task-based sampling.',
        inputSchema: {
            type: 'object',
            properties: { topic: { type: 'string', description: 'Topic for the haiku', default: 'nature' } }
        },
        execution: { taskSupport: 'required' }
    },
    {
        name: 'external_job',
        description: 'Simulates an external job system with custom task state lookup. Demonstrates the external proxy pattern.',
        inputSchema: {
            type: 'object',
            properties: { job_id: { type: 'string', description: 'External job ID to track', default: 'job-001' } }
        },
        execution: { taskSupport: 'required' }
    }
];

// ============================================================================
// Status notification helper — sends notifications/tasks/status after
// every status change (Phase 6 parity with RequestTaskStore pattern)
// ============================================================================

async function updateStatus(server, taskId, status, statusMessage, sessionId) {
    await taskStore.updateTaskStatus(taskId, status, statusMessage, sessionId);
    const task = await taskStore.getTask(taskId, sessionId);
    if (task) {
        try {
            await server.notification({ method: 'notifications/tasks/status', params: task });
        } catch (e) {
            // Notification delivery is best-effort — don't fail the operation
        }
    }
}

async function storeResult(server, taskId, status, result, sessionId) {
    await taskStore.storeTaskResult(taskId, status, result, sessionId);
    const task = await taskStore.getTask(taskId, sessionId);
    if (task) {
        try {
            await server.notification({ method: 'notifications/tasks/status', params: task });
        } catch (e) {
            // Best-effort
        }
    }
}

// ============================================================================
// Tool handlers
// ============================================================================

async function handleToolCall(server, request, ctx) {
    const { name, arguments: args } = request.params;
    const taskParams = request.params.task;

    if (name === 'greet') {
        if (taskParams) throw new Error('Tool "greet" does not support task invocation');
        return { content: [{ type: 'text', text: `Hello, ${args?.name || 'World'}!` }] };
    }

    if (name === 'slow_compute') {
        const seconds = args?.seconds || 3;
        const label = args?.label || 'default';

        if (!taskParams) {
            console.log(`[slow_compute] sync: sleeping ${seconds}s...`);
            await new Promise(r => setTimeout(r, seconds * 1000));
            console.log(`[slow_compute] finished "${label}"`);
            return { content: [{ type: 'text', text: `Computation "${label}" completed after ${seconds} seconds. Result: 42.` }] };
        }

        const task = await taskStore.createTask(
            { ttl: taskParams.ttl, pollInterval: taskParams.pollInterval ?? 1000 },
            ctx.mcpReq.id, request, ctx.sessionId
        );
        console.log(`[slow_compute] async: task ${task.taskId}, sleeping ${seconds}s...`);

        // AbortController for cancellation propagation (Phase 5 parity).
        const ac = new AbortController();
        taskAbortControllers.set(task.taskId, ac);

        (async () => {
            try {
                for (let i = 1; i <= seconds; i++) {
                    // Check cancellation before each sleep.
                    if (ac.signal.aborted) {
                        console.log(`[slow_compute] cancelled "${label}" at ${i}/${seconds}`);
                        return;
                    }
                    await new Promise((resolve, reject) => {
                        const timer = setTimeout(resolve, 1000);
                        ac.signal.addEventListener('abort', () => { clearTimeout(timer); resolve(); }, { once: true });
                    });
                    if (ac.signal.aborted) {
                        console.log(`[slow_compute] cancelled "${label}" at ${i}/${seconds}`);
                        return;
                    }
                    try {
                        await server.notification({
                            method: 'notifications/progress',
                            params: { progressToken: task.taskId, progress: i, total: seconds }
                        });
                    } catch (e) { /* best-effort */ }
                }
                console.log(`[slow_compute] finished "${label}"`);
                await storeResult(server, task.taskId, 'completed', {
                    content: [{ type: 'text', text: `Computation "${label}" completed after ${seconds} seconds. Result: 42.` }]
                }, ctx.sessionId);
            } finally {
                taskAbortControllers.delete(task.taskId);
            }
        })();

        return { task };
    }

    if (name === 'failing_job') {
        if (!taskParams) throw new Error('Tool "failing_job" requires task invocation');
        const task = await taskStore.createTask(
            { ttl: taskParams.ttl, pollInterval: taskParams.pollInterval ?? 1000 },
            ctx.mcpReq.id, request, ctx.sessionId
        );
        (async () => {
            await new Promise(r => setTimeout(r, 1000));
            await storeResult(server, task.taskId, 'failed', {
                content: [{ type: 'text', text: 'simulated failure: job crashed' }], isError: true
            }, ctx.sessionId);
        })();
        return { task };
    }

    if (name === 'confirm_delete') {
        if (!taskParams) throw new Error('Tool "confirm_delete" requires task invocation');
        const task = await taskStore.createTask(
            { ttl: taskParams.ttl, pollInterval: taskParams.pollInterval ?? 1000 },
            ctx.mcpReq.id, request, ctx.sessionId
        );
        const filename = args?.filename || 'important.txt';
        console.log(`[confirm_delete] task ${task.taskId}`);

        (async () => {
            try {
                await updateStatus(server, task.taskId, 'input_required', undefined, ctx.sessionId);
                const result = await server.elicitInput({
                    message: `Are you sure you want to delete '${filename}'?`,
                    requestedSchema: { type: 'object', properties: { confirm: { type: 'boolean' } }, required: ['confirm'] },
                    mode: 'form'
                });
                await updateStatus(server, task.taskId, 'working', undefined, ctx.sessionId);
                const text = (result.action === 'accept' && result.content?.confirm)
                    ? `Deleted '${filename}'` : 'Deletion cancelled';
                await storeResult(server, task.taskId, 'completed', { content: [{ type: 'text', text }] }, ctx.sessionId);
            } catch (e) {
                await storeResult(server, task.taskId, 'failed', {
                    content: [{ type: 'text', text: `Error: ${e}` }], isError: true
                }, ctx.sessionId);
            }
        })();

        return { task };
    }

    if (name === 'write_haiku') {
        if (!taskParams) throw new Error('Tool "write_haiku" requires task invocation');
        const task = await taskStore.createTask(
            { ttl: taskParams.ttl, pollInterval: taskParams.pollInterval ?? 1000 },
            ctx.mcpReq.id, request, ctx.sessionId
        );
        const topic = args?.topic || 'nature';
        console.log(`[write_haiku] task ${task.taskId}`);

        (async () => {
            try {
                await updateStatus(server, task.taskId, 'input_required', undefined, ctx.sessionId);
                const result = await server.createMessage({
                    messages: [{ role: 'user', content: { type: 'text', text: `Write a haiku about ${topic}` } }],
                    maxTokens: 50
                });
                await updateStatus(server, task.taskId, 'working', undefined, ctx.sessionId);
                const haiku = result.content?.text || 'No response';
                await storeResult(server, task.taskId, 'completed', {
                    content: [{ type: 'text', text: `Haiku about ${topic}:\n${haiku}` }]
                }, ctx.sessionId);
            } catch (e) {
                await storeResult(server, task.taskId, 'failed', {
                    content: [{ type: 'text', text: `Error: ${e}` }], isError: true
                }, ctx.sessionId);
            }
        })();

        return { task };
    }

    if (name === 'external_job') {
        if (!taskParams) throw new Error('Tool "external_job" requires task invocation');
        const task = await taskStore.createTask(
            { ttl: taskParams.ttl, pollInterval: taskParams.pollInterval ?? 1000 },
            ctx.mcpReq.id, request, ctx.sessionId
        );
        const jobId = args?.job_id || 'job-001';
        console.log(`[external_job] started external job ${jobId}, task ${task.taskId}`);
        (async () => {
            await new Promise(r => setTimeout(r, 1000));
            console.log(`[external_job] external job ${jobId} completed`);
            await storeResult(server, task.taskId, 'completed', {
                content: [{ type: 'text', text: `External job ${jobId} completed` }]
            }, ctx.sessionId);
        })();
        return { task };
    }

    throw new Error(`Unknown tool: ${name}`);
}

// ============================================================================
// Server factory — registers tools + tasks/* handlers on a fresh TS SDK Server
// ============================================================================

function createMCPServer() {
    const server = new Server(
        { name: 'tasks-demo-ts', version: '0.1.0' },
        { capabilities: { tools: {}, tasks: { requests: { tools: { call: {} } } } } }
    );

    server.setRequestHandler('tools/list', async () => ({ tools: TOOLS }));
    server.setRequestHandler('tools/call', (req, ctx) => handleToolCall(server, req, ctx));

    server.setRequestHandler('tasks/get', async (req, ctx) => {
        const task = await taskStore.getTask(req.params.taskId, ctx.sessionId);
        if (!task) throw new Error(`Task ${req.params.taskId} not found`);
        return task;
    });

    server.setRequestHandler('tasks/list', async (req, ctx) => {
        return await taskStore.listTasks(req.params?.cursor, ctx.sessionId);
    });

    server.setRequestHandler('tasks/cancel', async (req, ctx) => {
        const task = await taskStore.getTask(req.params.taskId, ctx.sessionId);
        if (!task) throw new Error(`Task ${req.params.taskId} not found`);
        if (isTerminal(task.status)) throw new Error(`Cannot cancel terminal task: ${task.status}`);
        // Abort the background goroutine (Phase 5 parity).
        const ac = taskAbortControllers.get(req.params.taskId);
        if (ac) ac.abort();
        await updateStatus(server, req.params.taskId, 'cancelled', 'task was cancelled', ctx.sessionId);
        return await taskStore.getTask(req.params.taskId, ctx.sessionId);
    });

    server.setRequestHandler('tasks/result', async (req, ctx) => {
        const { taskId } = req.params;
        while (true) {
            const task = await taskStore.getTask(taskId, ctx.sessionId);
            if (!task) throw new Error(`Task ${taskId} not found`);
            if (isTerminal(task.status)) {
                const result = await taskStore.getTaskResult(taskId, ctx.sessionId);
                return { ...result, _meta: { ...result._meta, [RELATED_TASK_META_KEY]: { taskId } } };
            }
            await new Promise(r => setTimeout(r, 1000));
        }
    });

    return server;
}

// ============================================================================
// HTTP transport (minimal — same pattern as TS SDK examples)
// ============================================================================

const httpServer = createServer(async (req, res) => {
    if (req.url !== '/mcp') { res.writeHead(404); res.end(); return; }

    const sid = req.headers['mcp-session-id'];
    if (req.method === 'GET' || req.method === 'DELETE') {
        if (sid && transports[sid]) await transports[sid].handleRequest(req, res);
        else { res.writeHead(400); res.end(); }
        return;
    }
    if (req.method !== 'POST') { res.writeHead(405); res.end(); return; }

    const chunks = [];
    for await (const c of req) chunks.push(c);
    const body = JSON.parse(Buffer.concat(chunks).toString());

    if (sid && transports[sid]) {
        await transports[sid].handleRequest(req, res, body);
    } else if (!sid && body.method === 'initialize') {
        const transport = new NodeStreamableHTTPServerTransport({
            sessionIdGenerator: () => randomUUID(),
            onsessioninitialized: (id) => { transports[id] = transport; }
        });
        transport.onclose = () => { if (transport.sessionId) delete transports[transport.sessionId]; };
        await createMCPServer().connect(transport);
        await transport.handleRequest(req, res, body);
    } else {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ jsonrpc: '2.0', error: { code: -32000, message: 'Bad request' }, id: null }));
    }
});

httpServer.listen(PORT, () => {
    console.log(`TS reference server on http://localhost:${PORT}/mcp`);
    console.log('Tools: greet, slow_compute, failing_job, confirm_delete, write_haiku, external_job');
});

process.on('SIGINT', () => { process.exit(0); });
