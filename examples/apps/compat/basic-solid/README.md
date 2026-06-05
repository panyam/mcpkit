# basic-solid — same App, Solid iframe

Rung 2 on the [examples ladder](../README.md#reading-order--examples-ladder).
Same wire surface as [`basic-vanillajs`](../basic-vanillajs/README.md);
the iframe is built with Solid (fine-grained reactivity, no virtual
DOM).

## What it shows

The MCP protocol surface doesn't care how the iframe is built. Tool
name, schema, resource URI, and `_meta.ui` shape are identical to
basic-vanillajs — only the HTML payload differs. Demonstrates that
mcpkit hosts can drive a Solid-based App with no special handling.

## Run it

```bash
# mcpkit-Go fixture + MCPJam (default — wire-level inspection)
make demo-app EXAMPLE=basic-server-solid

# Same Go fixture rendered in basic-host (iframe + bridge JS)
RENDERER=basic-host make demo-app EXAMPLE=basic-server-solid

# Compare against upstream's TS reference server
make demo-upstream EXAMPLE=basic-server-solid

# Strict parity check (visual baseline + tools/list diff, requires Docker)
EXAMPLE=basic-server-solid make test-apps-playwright-docker
```

## Prompts to try

Connect to `Basic MCP App Server (Solid)`, then paste any of these:

```
What's the current server time?
```

![basic-solid App rendered in basic-host: Solid-built iframe showing the ISO timestamp from get-time](screenshots/01-get-time.png)

```
Get the current time and tell me what day of the week that is.
```

```
Use the get-time tool.
```

The model calls `get-time`; the Solid iframe renders the result and
provides a button to call the tool again from the App side.

### Direct tool call (no LLM needed)

Same as [basic-vanillajs](../basic-vanillajs/README.md#direct-tool-call-no-llm-needed)
— select `get-time`, call with empty input, verify
`structuredContent.time` is an ISO 8601 string.

## What to look at next

- [`basic-vanillajs`](../basic-vanillajs/README.md) — the no-framework
  baseline.
- Other rung-2 framework variants:
  [`basic-preact`](../basic-preact/README.md) ·
  [`basic-react`](../basic-react/README.md) ·
  [`basic-svelte`](../basic-svelte/README.md) ·
  [`basic-vue`](../basic-vue/README.md).
- [`quickstart`](../quickstart/README.md) — same `get-time` tool, but
  upstream's "quickstart" template (default build setup).
