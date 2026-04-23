# MCP Elicitation Conformance Suite

Tests any MCP server that implements elicitation, including URL-mode elicitation (SEP-1036). Uses the official [MCP TypeScript SDK](https://github.com/modelcontextprotocol/typescript-sdk) client.

## Prerequisites

The target server MUST register these tools:

| Tool | Mode | Behavior |
|------|------|----------|
| `test_elicitation` | form | Sends `elicitation/create` with JSON schema, returns user input |
| `test_elicitation_url_mode` | url | Sends `elicitation/create` with `mode:"url"`, `url`, `elicitationId` |
| `test_elicitation_complete_notification` | url | URL elicitation + sends `notifications/elicitation/complete` |
| `test_elicitation_mode_default_form` | form | Sends `elicitation/create` with no `mode` field (backwards compat) |

The mcpkit test server (`cmd/testserver`) provides all of these tools.

## Setup

```bash
cd conformance
npm install
```

## Usage

```bash
# Start the test server
STREAMABLE=1 PORT=8080 go run ./cmd/testserver &

# Run conformance scenarios
SERVER_URL=http://localhost:8080/mcp npx tsx --test elicitation/scenarios.test.ts

# Or via Makefile (starts its own server):
make testconf-elicitation
```

## Scenarios

5 scenarios covering form-mode backwards compatibility and URL-mode elicitation (SEP-1036).

### URL-Mode (SEP-1036)

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 01 | URL-mode round-trip | Server sends `mode:"url"` + `url` + `elicitationId`, client validates | Client sees `mode`, `url`, `elicitationId`; no `requestedSchema` |
| 03 | Completion notification | Server sends `notifications/elicitation/complete` with `elicitationId` | Client receives notification with matching `elicitationId` |
| 04 | URL mode rejected without capability | Form-only client causes server's `ElicitURL()` to fail | Server reports URL-mode not supported |

### Backwards Compatibility

| # | Scenario | What it tests | Expected |
|---|----------|---------------|----------|
| 02 | Omitted mode defaults to form | No `mode` field = form mode | `requestedSchema` present, `mode` absent or `"form"` |
| 05 | Form mode works with URL-capable client | Existing form elicitation with client that also declares URL support | Standard form elicitation completes normally |

## Status

5/5 scenarios passing against mcpkit test server.
