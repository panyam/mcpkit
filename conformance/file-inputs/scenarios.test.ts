/**
 * SEP-2356 — File Inputs — Conformance Scenarios
 *
 * Verifies the wire-format contract for declarative file inputs:
 *
 *   - Tool input properties of `{type: "string", format: "uri"}` carrying
 *     the `x-mcp-file` JSON Schema extension keyword are advertised to
 *     clients that declare the `fileInputs` capability.
 *   - Servers MUST NOT include `x-mcp-file` in tool schemas (or
 *     elicitation `requestedSchema`) for clients without the capability.
 *     Per spec interpretation locked here: STRIP the keyword, KEEP the
 *     property visible as a plain `string`/`uri` so the tool stays
 *     callable on clients that haven't adopted SEP-2356.
 *   - Files travel as RFC 2397 base64 data URIs with an optional
 *     percent-encoded `name=` parameter:
 *         data:<mediatype>;name=<pct-encoded>;base64,<payload>
 *   - Servers reject oversized payloads with JSON-RPC `-32602` and
 *     structured `data: {reason: "file_too_large", actualSize, maxSize}`.
 *   - Servers reject MIME mismatches with `-32602` and
 *     `data: {reason: "file_type_not_accepted", mediaType, accept}`.
 *   - Both single-file and array-of-files inputs work; filenames with
 *     special characters round-trip through percent-encoding.
 *
 * Server fixture (one process):
 *   examples/file-inputs/file-inputs-demo --serve --addr=:18097
 *
 * The fixture registers three tools whose handlers exercise the SEP-2356
 * surface:
 *   upload_image       (image/* , maxSize 5 MiB)  — single-file picker
 *   analyze_documents  (.pdf / application/pdf)   — array picker
 *   process_any_file   (no constraints)           — unrestricted
 *
 * Apps-mode wrappers (`apps_upload_image`, `apps_analyze_documents`) are
 * not exercised here — they're bridge-mediated UX, not wire-format
 * concerns.
 *
 * Server URL comes from env (set by the Makefile target
 * `testconf-file-inputs`):
 *
 *   SERVER_URL — default http://localhost:18097/mcp
 *
 * Usage (manual — usually invoked via `make testconf-file-inputs`):
 *
 *   SERVER_URL=http://localhost:18097/mcp \
 *     npx tsx --test file-inputs/scenarios.test.ts
 */

import { describe, test } from 'node:test';
import { strict as assert } from 'node:assert';

const SERVER_URL = process.env.SERVER_URL || 'http://localhost:18097/mcp';

// =============================================================================
// Raw JSON-RPC plumbing — bypasses the SDK so we can inspect (and craft)
// wire shapes the SDK might validate-and-strip.
// =============================================================================

let nextId = 1;

interface InitOptions {
    /** Whether to declare the `fileInputs` capability in initialize. */
    fileInputs?: boolean;
}

async function initSession(opts: InitOptions = {}): Promise<string> {
    const capabilities: Record<string, unknown> = {};
    if (opts.fileInputs) capabilities.fileInputs = {};

    const resp = await fetch(SERVER_URL, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'Accept': 'application/json' },
        body: JSON.stringify({
            jsonrpc: '2.0', id: 'init', method: 'initialize',
            params: {
                protocolVersion: '2025-11-25',
                clientInfo: { name: 'file-inputs-conformance', version: '1.0' },
                capabilities,
            },
        }),
    });
    const sid = resp.headers.get('mcp-session-id') || '';
    if (!sid) throw new Error(`initialize at ${SERVER_URL} missing Mcp-Session-Id`);
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

interface RawCallResult {
    /** Set when the server returned a JSON-RPC `result`. */
    result?: any;
    /** Set when the server returned a JSON-RPC `error`. */
    error?: { code: number; message: string; data?: any };
}

async function rawCall(sid: string, method: string, params: any = null): Promise<RawCallResult> {
    const id = nextId++;
    const resp = await fetch(SERVER_URL, {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
            'Accept': 'text/event-stream, application/json',
            'Mcp-Session-Id': sid,
        },
        body: JSON.stringify({ jsonrpc: '2.0', id, method, params }),
    });
    const ct = resp.headers.get('content-type') || '';
    let body: any;
    if (ct.includes('text/event-stream')) {
        const text = await resp.text();
        for (const line of text.split('\n')) {
            const trimmed = line.trim();
            if (trimmed.startsWith('data:')) {
                const payload = trimmed.slice(5).trimStart();
                if (payload.startsWith('{')) {
                    const parsed = JSON.parse(payload);
                    if (parsed.id === id) { body = parsed; break; }
                }
            }
        }
    } else {
        body = await resp.json();
    }
    if (!body) throw new Error(`No response for ${method}`);
    return { result: body.result, error: body.error };
}

// =============================================================================
// Helpers — data URI synthesis and schema walking
// =============================================================================

/** Match Go's `url.PathEscape` — encodes `( ) ! * '` which encodeURIComponent leaves alone. */
function pctEncodePathLike(s: string): string {
    return encodeURIComponent(s).replace(
        /[!'()*]/g,
        (ch) => '%' + ch.charCodeAt(0).toString(16).toUpperCase(),
    );
}

/** Build a base64 data URI matching `core.EncodeDataURI` byte-for-byte. */
function makeDataURI(mediaType: string, filename: string, bytes: Buffer | string): string {
    const buf = typeof bytes === 'string' ? Buffer.from(bytes, 'utf8') : bytes;
    const namePart = filename ? `;name=${pctEncodePathLike(filename)}` : '';
    return `data:${mediaType}${namePart};base64,${buf.toString('base64')}`;
}

/** Walk a tool's inputSchema and return the set of property paths that
 * carry the `x-mcp-file` keyword. Used to assert presence/absence of the
 * keyword across single, array, and nested cases. */
function findFileInputPaths(inputSchema: any): string[] {
    const found: string[] = [];
    function walk(node: any, path: string): void {
        if (node == null || typeof node !== 'object') return;
        if (Object.prototype.hasOwnProperty.call(node, 'x-mcp-file')) {
            found.push(path);
        }
        if (node.properties && typeof node.properties === 'object') {
            for (const [k, v] of Object.entries(node.properties)) {
                walk(v, path ? `${path}.${k}` : k);
            }
        }
        if (node.items) {
            walk(node.items, `${path}[]`);
        }
    }
    walk(inputSchema, '');
    return found;
}

/** Pull a tool definition from a `tools/list` result by name. */
function findTool(toolsListResult: any, name: string): any {
    const tool = (toolsListResult.tools || []).find((t: any) => t.name === name);
    if (!tool) throw new Error(`tool ${name} not found in tools/list result`);
    return tool;
}

/** Pull the first text content block from a `tools/call` result. */
function extractText(callResult: any): string {
    const content = (callResult.content || []) as Array<{ type?: string; text?: string }>;
    return content.map((c) => (c.type === 'text' ? c.text || '' : '')).join('\n');
}

// =============================================================================
// Scenarios
// =============================================================================

describe('SEP-2356 File Inputs', () => {

    // -------------------------------------------------------------------------
    // Capability-gated schema visibility
    // -------------------------------------------------------------------------

    // verifies: when the client declares `fileInputs`, the server advertises
    // `x-mcp-file` on every file-input property in tools/list — including the
    // array-items shape used by `analyze_documents`.
    test('file-inputs-01: client with fileInputs cap sees x-mcp-file', async () => {
        const sid = await initSession({ fileInputs: true });
        const list = (await rawCall(sid, 'tools/list')).result;

        const upload = findTool(list, 'upload_image');
        const uploadPaths = findFileInputPaths(upload.inputSchema);
        assert.deepEqual(uploadPaths, ['image'],
            `upload_image: expected x-mcp-file on .image, found at ${JSON.stringify(uploadPaths)}`);

        const analyze = findTool(list, 'analyze_documents');
        const analyzePaths = findFileInputPaths(analyze.inputSchema);
        assert.deepEqual(analyzePaths, ['documents[]'],
            `analyze_documents: expected x-mcp-file on .documents[], found at ${JSON.stringify(analyzePaths)}`);

        const proc = findTool(list, 'process_any_file');
        const procPaths = findFileInputPaths(proc.inputSchema);
        assert.deepEqual(procPaths, ['file'],
            `process_any_file: expected x-mcp-file on .file, found at ${JSON.stringify(procPaths)}`);
    });

    // verifies: when the client does NOT declare `fileInputs`, the server
    // strips the `x-mcp-file` keyword from every tool schema BUT keeps the
    // properties themselves visible as plain string/uri so legacy clients
    // can still call the tools (rendering a text input as fallback).
    // Awaiting #362 (SEP-2356 A4 — capability gating).
    test('file-inputs-02: client without cap does NOT see x-mcp-file (but tools stay visible)', async () => {
        const sid = await initSession({ fileInputs: false });
        const list = (await rawCall(sid, 'tools/list')).result;

        for (const toolName of ['upload_image', 'analyze_documents', 'process_any_file']) {
            const tool = findTool(list, toolName);
            const paths = findFileInputPaths(tool.inputSchema);
            assert.deepEqual(paths, [],
                `${toolName}: x-mcp-file MUST be stripped for clients without fileInputs cap; found at ${JSON.stringify(paths)}`);

            // Property still visible — interpretation locked: strip keyword,
            // not the whole property. Verify the underlying string/uri schema
            // remains so the tool stays callable.
            const props = tool.inputSchema.properties || {};
            const required = tool.inputSchema.required || [];
            for (const name of required) {
                assert.ok(props[name] != null,
                    `${toolName}: required property "${name}" was hidden along with x-mcp-file (interpretation: keyword strip only)`);
            }
        }
    });

    // -------------------------------------------------------------------------
    // Wire-format round-trip
    // -------------------------------------------------------------------------

    // verifies: a valid file upload round-trips end-to-end. Server must
    // decode the data URI, recover the bytes / media type / filename, and
    // include them in the tool result. Uses the smallest valid PNG so the
    // test runs fast even on slow CI.
    test('file-inputs-03: valid file upload succeeds with metadata round-trip', async () => {
        const sid = await initSession({ fileInputs: true });

        // 1×1 transparent PNG (67 bytes) — same fixture shape used by
        // examples/file-inputs/testdata/.
        const png = Buffer.from([
            0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
            0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
            0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
            0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
            0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41,
            0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
            0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
            0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
            0x42, 0x60, 0x82,
        ]);
        const uri = makeDataURI('image/png', 'pixel.png', png);

        const out = (await rawCall(sid, 'tools/call', {
            name: 'upload_image',
            arguments: { image: uri, caption: 'conformance fixture' },
        })).result;

        assert.equal(out.isError, undefined,
            `tool returned isError; result=${JSON.stringify(out)}`);
        const text = extractText(out);
        assert.match(text, /pixel\.png/, 'response must echo the decoded filename');
        assert.match(text, /image\/png/, 'response must echo the decoded media type');
        assert.match(text, /\b67\b/, 'response must echo the decoded byte count (67 bytes)');
    });

    // verifies: when the descriptor declares maxSize=5 MiB and the client
    // sends a payload above that, the server returns JSON-RPC -32602 with
    // structured data: {reason, actualSize, maxSize}. Spec contract locked
    // here so error data shape stays consistent across reference impls.
    // Awaiting #361 (SEP-2356 A3 — server validation).
    test('file-inputs-04: oversized file rejects with -32602 + reason file_too_large', async () => {
        const sid = await initSession({ fileInputs: true });

        const maxSize = 5 * 1024 * 1024; // upload_image's declared maxSize
        const oversized = Buffer.alloc(maxSize + 1024); // 5 MiB + 1 KiB
        const uri = makeDataURI('image/png', 'too-big.png', oversized);

        const out = await rawCall(sid, 'tools/call', {
            name: 'upload_image',
            arguments: { image: uri },
        });

        assert.ok(out.error,
            `expected JSON-RPC error; got result=${JSON.stringify(out.result)}`);
        assert.equal(out.error!.code, -32602,
            `error.code = ${out.error!.code}, want -32602`);
        const data = out.error!.data || {};
        assert.equal(data.reason, 'file_too_large',
            `error.data.reason = ${JSON.stringify(data.reason)}, want "file_too_large"`);
        assert.equal(typeof data.actualSize, 'number',
            'error.data.actualSize MUST be a number');
        assert.ok(data.actualSize > maxSize,
            `actualSize ${data.actualSize} should exceed maxSize ${maxSize}`);
        assert.equal(data.maxSize, maxSize,
            `error.data.maxSize = ${data.maxSize}, want ${maxSize}`);
    });

    // verifies: when the descriptor declares accept=["image/*"] and the
    // client sends a text/plain payload, the server returns -32602 with
    // data: {reason: "file_type_not_accepted", mediaType, accept}.
    // Awaiting #361 (SEP-2356 A3 — server validation).
    test('file-inputs-05: wrong MIME rejects with -32602 + reason file_type_not_accepted', async () => {
        const sid = await initSession({ fileInputs: true });

        const uri = makeDataURI('text/plain', 'not-an-image.txt', 'hello world');
        const out = await rawCall(sid, 'tools/call', {
            name: 'upload_image',
            arguments: { image: uri },
        });

        assert.ok(out.error,
            `expected JSON-RPC error; got result=${JSON.stringify(out.result)}`);
        assert.equal(out.error!.code, -32602,
            `error.code = ${out.error!.code}, want -32602`);
        const data = out.error!.data || {};
        assert.equal(data.reason, 'file_type_not_accepted',
            `error.data.reason = ${JSON.stringify(data.reason)}, want "file_type_not_accepted"`);
        assert.equal(data.mediaType, 'text/plain',
            `error.data.mediaType = ${JSON.stringify(data.mediaType)}, want "text/plain"`);
        assert.ok(Array.isArray(data.accept),
            'error.data.accept MUST be an array of patterns the server accepts');
        assert.ok(data.accept.includes('image/*'),
            `error.data.accept = ${JSON.stringify(data.accept)} should include "image/*"`);
    });

    // verifies: array-of-files input (analyze_documents.documents) accepts
    // multiple data URIs in one call and the server decodes each. Confirms
    // FileInputArrayProperty's `items.x-mcp-file` shape isn't a special case
    // that gets dropped on the wire.
    test('file-inputs-06: multi-file array input handles multiple data URIs', async () => {
        const sid = await initSession({ fileInputs: true });

        const pdf = (label: string) => Buffer.from(`%PDF-1.4\n% ${label}\n%%EOF\n`, 'utf8');
        const docs = [
            makeDataURI('application/pdf', 'contract.pdf', pdf('contract')),
            makeDataURI('application/pdf', 'appendix.pdf', pdf('appendix')),
        ];

        const out = (await rawCall(sid, 'tools/call', {
            name: 'analyze_documents',
            arguments: { documents: docs },
        })).result;

        assert.equal(out.isError, undefined,
            `tool returned isError; result=${JSON.stringify(out)}`);
        const text = extractText(out);
        assert.match(text, /contract\.pdf/, 'response must echo first document filename');
        assert.match(text, /appendix\.pdf/, 'response must echo second document filename');
        assert.match(text, /application\/pdf/, 'response must echo PDF media type');
    });

    // verifies: filenames with characters outside the unreserved set
    // (parens, spaces, quotes) round-trip end-to-end. Catches encoders that
    // diverge from `url.PathEscape` (e.g., a JS implementation using only
    // `encodeURIComponent`, which leaves parens unescaped).
    test('file-inputs-07: filename with special chars round-trips via percent-encoding', async () => {
        const sid = await initSession({ fileInputs: true });

        const filename = "my photo (1) ' .png";
        // Confirm our local encoder produced the expected wire form before
        // we send it — protects the test itself against a regression in
        // pctEncodePathLike.
        const uri = makeDataURI('image/png', filename, Buffer.from([0x89, 0x50, 0x4e, 0x47]));
        assert.match(uri, /;name=my%20photo%20%281%29%20%27%20\.png;/,
            `local encoder output not in expected shape: ${uri}`);

        const out = (await rawCall(sid, 'tools/call', {
            name: 'process_any_file',
            arguments: { file: uri },
        })).result;

        assert.equal(out.isError, undefined,
            `tool returned isError; result=${JSON.stringify(out)}`);
        const text = extractText(out);
        // Server-side `core.DecodeDataURI` reverses the percent-encoding,
        // so the human-readable filename must reappear verbatim.
        assert.ok(text.includes(filename),
            `response must contain decoded filename ${JSON.stringify(filename)}; got: ${JSON.stringify(text)}`);
    });
});
