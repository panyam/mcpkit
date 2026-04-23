/**
 * MCP URL-Mode Elicitation Conformance Scenarios (SEP-1036)
 *
 * Tests any MCP server that implements URL-mode elicitation.
 * Uses the official MCP TypeScript SDK client — if these scenarios pass,
 * the server is conformant with SEP-1036.
 *
 * Usage:
 *   cd conformance && npm install
 *   SERVER_URL=http://localhost:8080/mcp npx tsx --test elicitation-url/scenarios.test.ts
 *
 * The server MUST register these tools:
 *   - test_elicitation_url_mode — sends URL-mode elicitation/create
 *   - test_elicitation_complete_notification — sends URL elicitation + completion notification
 *   - test_elicitation_mode_default_form — sends form-mode (no mode field) for backwards compat
 *   - test_elicitation (existing) — sends form-mode elicitation
 */

import { describe, test, before, after } from 'node:test';
import { strict as assert } from 'node:assert';
import { Client, StreamableHTTPClientTransport } from '@modelcontextprotocol/client';

const SERVER_URL = process.env.SERVER_URL || 'http://localhost:8080/mcp';

let client: Client;

// Track notifications received during tests.
const notifications: Array<{ method: string; params: any }> = [];

before(async () => {
    const transport = new StreamableHTTPClientTransport(new URL(SERVER_URL));
    client = new Client(
        { name: 'mcp-elicitation-url-conformance', version: '1.0.0' },
        { capabilities: { elicitation: { form: {}, url: {} } } }
    );

    // Listen for all notifications.
    client.setNotificationHandler('notifications/elicitation/complete', async (notification: any) => {
        notifications.push({ method: 'notifications/elicitation/complete', params: notification.params });
    });

    await client.connect(transport);
});

after(async () => {
    await client.close();
});

// ============================================================================
// Helper: call a tool and return the text result
// ============================================================================

async function callTool(name: string, args: Record<string, unknown> = {}): Promise<string> {
    const result = await client.request(
        {
            method: 'tools/call',
            params: { name, arguments: args },
        },
        {} as any
    );
    const content = (result as any).content;
    assert.ok(Array.isArray(content), 'result should have content array');
    const textItem = content.find((c: any) => c.type === 'text');
    assert.ok(textItem, 'result should have a text content item');
    return textItem.text;
}

// ============================================================================
// Scenarios
// ============================================================================

describe('MCP URL-Mode Elicitation Conformance (SEP-1036)', () => {

    // Scenario 1: URL-mode elicitation round-trip
    // The server sends elicitation/create with mode="url", url, and elicitationId.
    // The client handler verifies these fields and returns "accept".
    test('scenario 1: URL-mode elicitation round-trip', async () => {
        // Set up handler that validates URL-mode fields.
        client.setRequestHandler('elicitation/create', async (request: any) => {
            const params = request.params;
            assert.equal(params.mode, 'url', 'mode should be "url"');
            assert.ok(params.url, 'url field must be present');
            assert.ok(params.elicitationId, 'elicitationId field must be present');
            assert.ok(params.url.startsWith('https://'), 'url should be HTTPS');
            // requestedSchema must NOT be present for URL mode.
            assert.equal(params.requestedSchema, undefined, 'requestedSchema must not be set for URL mode');
            return { action: 'accept' as const };
        });

        const text = await callTool('test_elicitation_url_mode');
        assert.match(text, /action=accept/, 'server should see accept action');
    });

    // Scenario 2: Form-mode backwards compatibility (omitted mode = form)
    // When mode is not set, it defaults to form. The server sends a plain
    // elicitation/create with requestedSchema and no mode field.
    test('scenario 2: omitted mode defaults to form (backwards compat)', async () => {
        client.setRequestHandler('elicitation/create', async (request: any) => {
            const params = request.params;
            // mode should be absent or "form"
            assert.ok(
                params.mode === undefined || params.mode === 'form',
                `mode should be absent or "form", got "${params.mode}"`
            );
            // requestedSchema should be present for form mode.
            assert.ok(params.requestedSchema, 'requestedSchema should be present for form mode');
            return {
                action: 'accept' as const,
                content: { color: 'blue' },
            };
        });

        const text = await callTool('test_elicitation_mode_default_form');
        assert.match(text, /action=accept/, 'server should see accept action');
    });

    // Scenario 3: Completion notification delivery
    // The server sends a URL elicitation then sends
    // notifications/elicitation/complete with the elicitationId.
    test('scenario 3: completion notification delivered', async () => {
        notifications.length = 0; // Clear prior notifications.

        client.setRequestHandler('elicitation/create', async (request: any) => {
            return { action: 'accept' as const };
        });

        const text = await callTool('test_elicitation_complete_notification');
        assert.match(text, /action=accept/, 'server should see accept action');

        // The completion notification may arrive asynchronously — wait briefly.
        await new Promise(r => setTimeout(r, 500));

        const completeNotifs = notifications.filter(
            n => n.method === 'notifications/elicitation/complete'
        );
        assert.ok(
            completeNotifs.length > 0,
            'should have received at least one elicitation complete notification'
        );
        const notif = completeNotifs[0];
        assert.ok(notif.params?.elicitationId, 'notification should have elicitationId');
    });

    // Scenario 4: Client declares form-only — URL mode rejected
    // A form-only client should cause the server's ElicitURL() to fail
    // because the client's capabilities don't include url mode.
    test('scenario 4: URL mode rejected when client lacks url capability', async () => {
        // Create a second client with form-only capability.
        const transport2 = new StreamableHTTPClientTransport(new URL(SERVER_URL));
        const formOnlyClient = new Client(
            { name: 'form-only-client', version: '1.0.0' },
            { capabilities: { elicitation: { form: {} } } }
        );

        formOnlyClient.setRequestHandler('elicitation/create', async () => {
            // Should not be called for URL mode.
            return { action: 'accept' as const };
        });

        await formOnlyClient.connect(transport2);

        try {
            const result = await formOnlyClient.request(
                {
                    method: 'tools/call',
                    params: { name: 'test_elicitation_url_mode', arguments: {} },
                },
                {} as any
            );
            const content = (result as any).content;
            const textItem = content?.find((c: any) => c.type === 'text');
            // The server should report that URL mode is not supported.
            assert.ok(
                textItem?.text?.includes('does not support URL-mode') ||
                textItem?.text?.includes('url elicitation failed'),
                `expected URL-not-supported error, got: "${textItem?.text}"`
            );
        } finally {
            await formOnlyClient.close();
        }
    });

    // Scenario 5: Existing form-mode elicitation still works
    // Verifies that the existing test_elicitation tool (form mode) still
    // works with a client that declares both form and url support.
    test('scenario 5: form-mode elicitation still works with url-capable client', async () => {
        client.setRequestHandler('elicitation/create', async (request: any) => {
            return {
                action: 'accept' as const,
                content: { name: 'conformance-test' },
            };
        });

        const text = await callTool('test_elicitation');
        assert.match(text, /conformance-test/, 'should contain the submitted name');
    });
});
