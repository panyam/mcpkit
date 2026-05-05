# URL Elicitation — Consent Approval Flow (UC1)

**EXPERIMENTAL** — Tracks SEP-2643 (Structured Authorization Denials), currently a draft. A scripted MCP host walking through the UC1 consent approval flow. Wire format may change as the SEP evolves.

## What you'll learn

- **Connect to the MCP server and initialize session** — Connect with a notification callback listening for notifications/elicitation/complete. The GET SSE stream receives server-pushed notifications.
- **Call access_protected_resource — denied with consent URL** — The consent middleware intercepts the call and returns -32042 (URLElicitationRequired) with a URL the user must visit to approve access.
- **Open consent URL → wait for approval notification → auto-retry** — The host opens the consent URL and waits for the server to send a notifications/elicitation/complete notification via the SSE stream. When it arrives, the host automatically retries with the authorizationContextId.

## Flow

```mermaid
sequenceDiagram
    participant Host as MCP Host (this client)
    participant Server as MCP Server (make serve)
    participant Browser as User Browser

    Note over Host,Browser: Step 1: Connect to the MCP server and initialize session
    Host->>Server: POST /mcp — initialize
    Server-->>Host: serverInfo + Mcp-Session-Id
    Host->>Server: GET /mcp — open SSE stream for notifications

    Note over Host,Browser: Step 2: Call access_protected_resource — denied with consent URL
    Host->>Server: tools/call: access_protected_resource
    Server-->>Host: error -32042 + consent URL + authzContextId

    Note over Host,Browser: Step 3: Open consent URL → wait for approval notification → auto-retry
    Host->>Browser: open consent URL
    Browser->>Server: POST /approve?ctx=...
    Server-->>Host: notifications/elicitation/complete (via SSE)
    Host->>Server: tools/call + _meta.authorizationContextId (auto-retry)
    Server-->>Host: Access granted to resource
```

## Steps

### Setup

Before running this demo, start the MCP server in a separate terminal:

```
Terminal 1:  make serve        # start the MCP server on :8080
Terminal 2:  make run          # run this demo
```

### Step 1: Connect to the MCP server and initialize session

Connect with a notification callback listening for notifications/elicitation/complete. The GET SSE stream receives server-pushed notifications.

### Step 2: Call access_protected_resource — denied with consent URL

The consent middleware intercepts the call and returns -32042 (URLElicitationRequired) with a URL the user must visit to approve access.

### Step 3: Open consent URL → wait for approval notification → auto-retry

The host opens the consent URL and waits for the server to send a notifications/elicitation/complete notification via the SSE stream. When it arrives, the host automatically retries with the authorizationContextId.

## Run it

```bash
go run ./examples/elicitation/
```

Pass `--non-interactive` to skip pauses:

```bash
go run ./examples/elicitation/ --non-interactive
```
