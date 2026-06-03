#!/usr/bin/env node
// apps-playwright-tools-diff.mjs
//
// Connect to two MCP servers via Streamable HTTP, fetch tools/list from each,
// normalize (deep-sort keys, sort tools by name), and unified-diff. Used by
// apps-playwright-docker-inner.sh to catch protocol-surface drift between a
// mcpkit-Go fixture and the upstream TypeScript reference server.
//
// Pixel-level baselines drift transparently when upstream changes their
// rendered output — both we and upstream regenerate against the new look, so
// nothing fires. The protocol surface (tool name + title + description +
// schemas + _meta.ui) doesn't have that escape hatch: any drift here means
// the fixture has fallen behind upstream.
//
// Usage:
//   node apps-playwright-tools-diff.mjs <a-label> <a-url> <b-label> <b-url>
//
// Exit codes:
//   0 — tools/list matches
//   1 — drift detected; unified diff printed to stderr
//   2 — usage error

import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import { execFileSync } from "node:child_process";
import { writeFileSync } from "node:fs";

async function listTools(url) {
    const client = new Client({ name: "drift-check", version: "0" }, {});
    const transport = new StreamableHTTPClientTransport(new URL(url));
    await client.connect(transport);
    const { tools } = await client.listTools();
    await client.close();
    return tools;
}

// Keys stripped before comparison — these are SDK-level emit differences
// that don't change semantics on the wire:
//
//   $schema             — mcpkit emits draft-2020-12 via invopop, upstream's
//                         TS SDK emits draft-07 via zod-to-json-schema.
//                         Presence matters for clients; value is the SDK's
//                         own draft choice.
//
//   additionalProperties — mcpkit's invopop omits (permissive default);
//                          upstream's zod-to-json-schema emits `false`
//                          (strict). Documented in core/schema.go.
//
//   propertyNames        — upstream's zod `z.record(z.string(), z.unknown())`
//                          emits `{"propertyNames": {"type": "string"}}` for
//                          string-keyed maps; mcpkit's `map[string]any`
//                          omits it. Both mean the same thing.
const IGNORE_KEYS = new Set(["$schema", "additionalProperties", "propertyNames"]);

// JSON Schema "integer" is a subtype of "number" — any integer value
// validates against both schemas. Different language SDKs make different
// emit choices: mcpkit's invopop reflects Go `int` → "integer" (more
// precise: Go has distinct numeric types); upstream's zod-to-json-schema
// always emits "number" (TypeScript's `number` is a 64-bit float, can't
// distinguish). Both descriptions are valid for integer-valued data; the
// comparator normalizes both sides to "number" so the SDK divergence
// doesn't manifest as drift.
function normalizeTypeValue(v) {
    return v === "integer" ? "number" : v;
}

function deepSortKeys(value, parentKey) {
    if (Array.isArray(value)) return value.map((v) => deepSortKeys(v));
    if (value && typeof value === "object") {
        const sorted = {};
        for (const k of Object.keys(value).sort()) {
            if (IGNORE_KEYS.has(k)) continue;
            sorted[k] = deepSortKeys(value[k], k);
        }
        return sorted;
    }
    if (parentKey === "type") return normalizeTypeValue(value);
    return value;
}

function normalize(tools) {
    return tools
        .slice()
        .sort((a, b) => a.name.localeCompare(b.name))
        .map(deepSortKeys);
}

const [aLabel, aUrl, bLabel, bUrl] = process.argv.slice(2);
if (!aUrl || !bUrl) {
    console.error("Usage: apps-playwright-tools-diff.mjs <a-label> <a-url> <b-label> <b-url>");
    process.exit(2);
}

const [aTools, bTools] = await Promise.all([listTools(aUrl), listTools(bUrl)]);
const aJson = JSON.stringify(normalize(aTools), null, 2);
const bJson = JSON.stringify(normalize(bTools), null, 2);

if (aJson === bJson) {
    console.log(`tools/list parity OK between ${aLabel} and ${bLabel} (${aTools.length} tools)`);
    process.exit(0);
}

const aPath = "/tmp/tools-diff-a.json";
const bPath = "/tmp/tools-diff-b.json";
writeFileSync(aPath, aJson);
writeFileSync(bPath, bJson);

console.error(`tools/list DRIFT DETECTED between ${aLabel} (${aUrl}) and ${bLabel} (${bUrl})`);
console.error("");
try {
    execFileSync(
        "diff",
        ["-u", "--label", aLabel, "--label", bLabel, aPath, bPath],
        { stdio: "inherit" },
    );
} catch {
    // diff returns non-zero when files differ — expected and is the signal we
    // want to surface, not an error from our perspective.
}
process.exit(1);
