# basic-preact — same App, Preact iframe

Rung 2 on the [examples ladder](../README.md#reading-order--examples-ladder).
Same wire surface as [`basic-vanillajs`](../basic-vanillajs/README.md);
the iframe is built with Preact instead of vanilla JS.

## What it shows

The MCP protocol surface doesn't care how the iframe is built. Tool
name, schema, resource URI, and `_meta.ui` shape are identical to
basic-vanillajs — only the HTML payload differs. Demonstrates that
mcpkit hosts can drive a Preact-based App with no special handling.

## Run it

```bash
make demo-app EXAMPLE=basic-server-preact
make inspect-app EXAMPLE=basic-server-preact
EXAMPLE=basic-server-preact make test-apps-playwright-docker
```

## Prompts to try

Connect to `Basic MCP App Server (Preact)`, then paste any of these:

```
What's the current server time?
```

![basic-preact App rendered in basic-host: Preact-built iframe showing the ISO timestamp from get-time](screenshots/01-get-time.png)

```
Get the current time and tell me what day of the week that is.
```

```
Use the get-time tool.
```

The model calls `get-time`; the Preact iframe renders the result and
provides a button to call the tool again from the App side.

### Direct tool call (no LLM needed)

Same as [basic-vanillajs](../basic-vanillajs/README.md#direct-tool-call-no-llm-needed)
— select `get-time`, call with empty input, verify
`structuredContent.time` is an ISO 8601 string.

## What to look at next

- [`basic-vanillajs`](../basic-vanillajs/README.md) — the no-framework
  baseline.
- Other rung-2 framework variants:
  [`basic-react`](../basic-react/README.md) ·
  [`basic-solid`](../basic-solid/README.md) ·
  [`basic-svelte`](../basic-svelte/README.md) ·
  [`basic-vue`](../basic-vue/README.md).
- [`quickstart`](../quickstart/README.md) — same `get-time` tool, but
  upstream's "quickstart" template (default build setup).
