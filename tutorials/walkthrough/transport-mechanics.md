# Transport mechanics: stdio vs. streamable HTTP

What the wire actually looks like, how server→client traffic flows on each transport, and how messages get correlated.

> **Kind:** root · **Assumes:** nothing (foundational)
> **Reachable from:** [bring-up](./bringup.md) phase 2–3, [README](./README.md)
> **Branches into:** (forthcoming) reverse-call, SSE resumption, batching
> **Spec:** [Base protocol](https://modelcontextprotocol.io/specification/2025-06-18) · [Transports](https://modelcontextprotocol.io/specification/2025-06-18) · **Code:** `core/jsonrpc.go`, `server/stdio_transport.go`, `server/streamable_transport.go`, `client/mrtr.go`, `server/event_ids.go`

## Preconditions

**None — this is a foundational root.** A reader needs general familiarity with HTTP, SSE, and JSON-RPC at a vocabulary level. No other roots required.

## What both transports share

JSON-RPC 2.0. Three message shapes — request, response, notification — covered in the [Correlation](#correlation-json-rpc-20-transport-agnostic) section below. Transports differ only on **framing** and on **how server→client traffic gets back to the client**. The message model is identical.

## Sessions, connections, and tool calls

Before getting into wire details, three layers worth keeping distinct:

| Concept | Scope | Lifetime owner |
|---------|-------|----------------|
| **Host process** | the user's chat session, possibly hours | host (e.g. Claude Desktop, an agent runtime) |
| **MCP session** | one client ↔ one server, established by `initialize` | host's MCP client + server |
| **Transport transaction** | a single TCP/HTTP exchange — one POST, one GET, … | transport library, mostly invisible |

An MCP session is **not per-tool-call.** It's per-server-per-host, kept alive as long as the host wants it. A typical chat:

1. Host reads its server config — say Jira, Slack, Glean.
2. Host opens **one MCP client per server.** Each runs [`initialize`](./bringup.md) and discovers tools via `tools/list`. Three concurrent sessions, possibly over different transports.
3. Host advertises a **flat namespace of tools** (`jira_search`, `slack_post`, `glean_lookup`, …) to the model, knowing per tool which session owns it.
4. When the model calls `jira_search`, the host routes that one call to the Jira session as a single POST (HTTP) or one message on the pipe (stdio). The session is reused for every subsequent call.

```mermaid
graph LR
    h[host] --> cj[client → Jira]
    h --> cs[client → Slack]
    h --> cg[client → Glean]
    cj -->|MCP session A| j[Jira server]
    cs -->|MCP session B| s[Slack server]
    cg -->|MCP session C| g[Glean server]
```

Sessions over hours are normal — the protocol is built for them. Host policy choices the spec doesn't mandate: eager-vs-lazy connect, pooling, when to tear down. mcpkit (and most hosts) default to "open on first use, keep alive for chat duration."

> [!NOTE]
> Even though every tool call is its own POST, an HTTP MCP "session" isn't a stateless request/response API. The `Mcp-Session-Id` ties many POSTs and the standing-GET back-channel together as one logical conversation. The bring-up cost (auth, `initialize`, `tools/list`) is paid once.

## stdio

```mermaid
graph LR
    H[host process] -- spawns --> S[server process]
    H -- "stdin (newline-delimited JSON)" --> S
    S -- "stdout (newline-delimited JSON)" --> H
    S -- "stderr (opaque logs, NOT protocol)" --> H
```

- **Bring-up:** fork/exec the configured command. Hook stdin/stdout/stderr. Done.
- **Framing:** newline-delimited JSON. Each message is one line, terminated by `\n`. Both sides treat every newline as a frame boundary.
- **Direction:** full-duplex from t=0. No upgrade, no negotiation, no headers, no session id.
- **Server→client traffic:** just appears on stdout, interleaved with responses. Notifications, reverse requests, responses — all the same channel.
- **stderr:** for the server's host-side logs. Not protocol traffic. The host may surface it to the user or drop it.

> [!NOTE]
> "Connection up" on stdio = "process running." There's no handshake at the transport level. The protocol-level [`initialize`](./bringup.md#4--initialize-handshake-transport-agnostic-protocol-level) handshake is the only handshake.

## Streamable HTTP

A single endpoint URL, three HTTP methods used together to simulate full-duplex:

- **POST** — client→server messages (and, optionally, the response stream tied to that POST)
- **GET** — long-lived server→client back-channel for unsolicited server-initiated traffic
- **DELETE** — explicit session termination (optional)

### POST: client→server (with optional streaming response)

```
POST /mcp HTTP/1.1
Host: example.com
Content-Type: application/json
Accept: application/json, text/event-stream
Mcp-Session-Id: abc123                    (after first response, if server uses sessions)
Authorization: Bearer eyJ...              (if auth required)

{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{...}}
```

> [!IMPORTANT]
> `Accept: application/json, text/event-stream` is required — **both** types must be listed. This is the client telling the server "I can take either type of response." Servers may reject POSTs that omit one.

Server response options:

- **Body has only responses/notifications (no requests inside):** `202 Accepted`, no body.
- **Body has at least one request:** server picks one of two response styles —
  - `Content-Type: application/json` + JSON-RPC response in the body. Connection closes. Used when no streaming is needed.
  - `Content-Type: text/event-stream` + SSE event stream — the "upgrade." Used when the server wants to interleave notifications, progress, or server-initiated requests with the eventual response.

> [!NOTE]
> The "upgrade" is **not** a WebSocket-style handshake. It's just *which Content-Type the server picks on the response*. Same endpoint, same POST, server decides per-request.

When the server picks SSE for a POST, the stream stays open until the server has nothing more to send for *this request*. During that window the server may emit:

- Notifications related to this call (e.g., `notifications/progress`)
- Server-initiated requests originated by the handler (e.g., `sampling/createMessage`, `elicitation/create`)
- The final JSON-RPC response

Then the stream closes.

### GET: long-lived server→client back-channel

For server-initiated messages **not** tied to a specific client request, the client opens a long-lived GET against the same endpoint, **after `initialize` has succeeded** (so the client has the session id, if any):

```
GET /mcp HTTP/1.1
Accept: text/event-stream
Mcp-Session-Id: abc123                    (mandatory if the server issued one)
Last-Event-ID: 42                         (optional, for resumption after reconnect)
```

Server returns `Content-Type: text/event-stream` and keeps it open. Each SSE event has an `id:` line so the client can resume with `Last-Event-ID` after a network blip. mcpkit: `server/event_ids.go`.

**The GET is independent of any POST.** It's typically opened once per session, right after `initialize`, and stays up regardless of what's happening on the POST side. POSTs come and go; the GET is the steady channel for unsolicited server→client traffic. *Q: if a POST upgraded to SSE for session X and that POST's stream then closes, does the GET on the same session close?* No — they are separate HTTP transactions over the same logical session. The session ends only on `DELETE`, on session-id invalidation by the server, or on host shutdown. POSTs ending is just transactions ending.

> [!NOTE]
> mcpkit's runtime models this directly. A handler's `requestFunc`/`notifyFunc` bound to a POST scope dies when that POST's response is sent. Background goroutines that need to outlive the POST must call `core.DetachForBackground(ctx)` to attach to the **session-level persistent push** — i.e., the standing GET back-channel. (See [CLAUDE.md → Gotchas → Background goroutines](../../CLAUDE.md).)

> [!NOTE]
> **Branch →** *(forthcoming)* SSE resumption. The `Last-Event-ID` mechanic, what the server has to remember for replay, and how mcpkit's event store handles in-flight responses across reconnects.

> [!NOTE]
> **Branch →** [`experimental/ext/events/`](../../experimental/ext/events/README.md) — mcpkit's MCP Events protocol exploration. Treats events as a first-class concept beyond raw SSE event-id replay. Out-of-scope here; visit if you're tracking where the protocol is heading.

> [!NOTE]
> So there are really **two SSE patterns** on streamable HTTP:
> 1. **Per-call SSE** — a POST's response upgrades to SSE for that one request's lifetime.
> 2. **Standing GET SSE** — long-lived back-channel for unsolicited server-initiated traffic.
>
> Same wire format, different lifecycles. Different journeys touch different ones.

### `Mcp-Session-Id`

**Generated by the server, not the client.** On the first response from the server (typically the response to the client's `initialize` POST), the server includes an `Mcp-Session-Id: <id>` header. The client stores it and echoes it on every subsequent POST, GET, and DELETE for the lifetime of the session. So the timeline is:

1. Client POSTs `initialize` (no `Mcp-Session-Id` yet — there isn't one).
2. Server responds; the response carries `Mcp-Session-Id: abc123`.
3. From now on, every POST/GET/DELETE from this client carries `Mcp-Session-Id: abc123`.
4. The standing GET (opened by the client after step 2) also carries it.

Servers MAY operate stateless (no session id at all) — many won't. Where there's no session id, there's no standing GET back-channel either; server-initiated traffic can only flow during a per-call SSE upgrade.

> [!CAUTION]
> **Target-incompatible (replacement):** the [Dec-2025 transport WG post](https://blog.modelcontextprotocol.io/posts/2025-12-19-mcp-transport-future/) moves toward a stateless transport with sessions elevated to the data layer. Transport-level `Mcp-Session-Id` is on the chopping block. Code that pins behavior to the header will need to migrate.

### Why HTTP needs all this scaffolding and stdio doesn't

HTTP is request/response by nature; full-duplex isn't free. Three pieces — POST, GET, `Mcp-Session-Id` — work together to recover what stdio gets for free from a bidirectional pipe.

## Correlation: JSON-RPC 2.0 (transport-agnostic)

### Layering

MCP layers cleanly:

```
+----------------------------+
| MCP semantics              |   tools, prompts, resources, sampling, …
+----------------------------+
| JSON-RPC 2.0 message model |   request / response / notification, id correlation
+----------------------------+
| Transport framing          |   newline-delimited JSON (stdio) | HTTP body / SSE event (HTTP)
+----------------------------+
| Transport bytes            |   pipe (stdio) | TCP+TLS (HTTP)
+----------------------------+
```

The **JSON-RPC payload format is transport-agnostic.** Both stdio and streamable HTTP carry the same JSON-RPC payloads — they only differ on framing and on how the receiver gets bytes off the channel. JSON-RPC could in principle be replaced (Connect, gRPC, MessagePack, CBOR, …) without changing the MCP semantics on top — but that would be a *spec-level* change, not a transport choice. As of the [2025-06-18 spec](https://modelcontextprotocol.io/specification/2025-06-18), JSON-RPC 2.0 is normative.

> [!NOTE]
> gRPC specifically would be an awkward fit — it has a different model (strongly-typed services, streaming as first-class, no real "notification"). Connect (Buf's HTTP/JSON-friendly cousin) maps more naturally. None are on the spec roadmap today.

### Wire shapes

Three:

| Shape | Has `id`? | Has `method`? | Has `result`/`error`? | Meaning |
|-------|-----------|---------------|-----------------------|---------|
| Request | yes | yes | no | I expect a response with the same `id` |
| Response | yes | no | yes (exactly one of) | Reply to a request with that `id` |
| Notification | no | yes | no | Fire-and-forget |

(Plus batches — a JSON array of any of the above. mcpkit handles them.)

### Per-direction ID space

Each side allocates IDs **independently**. Client's `id=5` and server's `id=5` are different things — they belong to different pending-request tables.

When a message arrives, the receiver dispatches by shape:

- Has `id` + `result`/`error` → response to a request *I* sent → look up my pending table by `id`, resolve the waiting caller.
- Has `id` + `method` → request *from the peer* → dispatch to a handler, eventually send a response with the same `id`.
- No `id` + `method` → notification → dispatch to a handler, no response.

mcpkit: type definitions in `core/jsonrpc.go`; correlation tables in `client/mrtr.go` and `server/mrtr.go`.

### Reverse-call origination

When a server originates a request to a client (e.g., `sampling/createMessage`), the request uses a **new id from the server's id space** — not a child or extension of the client's forward-request id.

The spec requires server-initiated requests to be *"in association with an originating client request"* — but **this association is not on the wire.** There is no `parent` field in JSON-RPC. The wire just sees a fresh request from the server with a new id. So how is the constraint enforced?

**Via the server's handler context, not the pending-id table.** When the server dispatches a forward request, it builds a *handler context* that knows "I am inside the handling of forward request id=N." A reverse call is only originatable through this context — and you can only get a context inside a forward call. So a reverse request that hits the wire is *necessarily* tied to a forward call by construction, even though nothing on the wire says so.

```mermaid
sequenceDiagram
    participant CC as client caller
    participant CL as client mrtr
    participant SV as server mrtr
    participant HC as handler context
    participant H as handler

    CC->>CL: Call("tools/call", args)
    CL->>SV: { id: 10, method: "tools/call", params }
    Note over CL: client.pending[10]<br/>= caller continuation
    SV->>HC: build context bound to fwd-id=10
    HC->>H: dispatch handler
    H->>HC: originate reverse call
    Note over HC: alloc server id=42<br/>record (42 originated-by 10)
    HC->>SV: send { id: 42, method: "sampling/createMessage", params }
    SV->>CL: same on the wire (no parent field)
    Note over SV: server.pending[42]<br/>= handler-resume continuation
    CL->>CC: dispatch sampling locally
    CC->>CL: { id: 42, result }
    CL->>SV: same on the wire
    SV->>HC: pending[42] resolves, resume handler
    HC->>H: return reverse-call result
    H->>HC: tool result
    HC->>SV: { id: 10, result }
    SV->>CL: forward response
    CL->>CC: resolve the original Call
```

So there are **two distinct things at play**:

1. **The pending-id table** (one per direction) maps `id → continuation`. It's flat — no tree structure, no parent links visible to it. Its job is to resolve incoming responses to the right waiter.
2. **The handler context** (server-side, built per forward request) carries "what forward call am I inside of?" It's how reverse calls get originated, and it carries enough state to propagate cancellation: if forward id=10 is cancelled, the context can find and cancel its outstanding reverse calls (e.g., id=42) by the back-pointer it recorded when originating them.

mcpkit's `core/handler_context.go` is where both concerns meet — it's the handle through which user-written handlers issue reverse calls, and it's where the forward-id-to-reverse-id relationship is recorded for cancellation propagation. Nothing of this leaks onto the wire.

> [!IMPORTANT]
> A reverse call attempted *outside* a handler context — e.g., from a background goroutine that has escaped its forward-request scope — is a programming error and a spec violation if it escapes onto the wire. mcpkit's `core.DetachForBackground(ctx)` is the supported way to keep using the session-level push channel from background work; see the GET section above.

> [!NOTE]
> **Branch →** *(forthcoming)* Reverse-call mechanics. Walks `tools/call → elicitation/create` end-to-end, with code references to `core/handler_context.go` and the mrtr origination path.

### Order of arrival ≠ order of sending

On a full-duplex transport (stdio, or SSE-streamed HTTP), either side may have many concurrent outstanding requests. Responses can arrive in any order. The pending-id table is what makes correlation work; FIFO assumptions will bite.

## End-state (what downstream pages can assume)

After reading this root, downstream pages can assume:

- You distinguish **host process / MCP session / transport transaction** as three different lifetimes. A session is per-server-per-host, may live for hours, and is reused across many tool calls.
- You can read a JSON-RPC message off the wire for either transport and tell whether it's a request, response, or notification.
- You understand the **layering**: MCP semantics > JSON-RPC message model > transport framing > transport bytes. Payload format is transport-agnostic.
- You know how server→client messages reach the client on each transport: always-open pipe (stdio), per-call SSE upgrade (HTTP POST), standing GET SSE (HTTP back-channel — independent of any POST, opened after `initialize`).
- You know `Mcp-Session-Id` is server-issued, returned on the response to `initialize`, mandatory on all subsequent client requests if issued.
- You know IDs are per-direction and that the pending-request table is what makes correlation work — flat, no parent links.
- You know reverse-call origination is gated by **handler context** (not the pending-id table, not anything on the wire). The handler context records the forward→reverse association for cancellation propagation.

## Leads to

Roots that build on this end-state:

- **(forthcoming) Per-request anatomy** — uses the wire model and correlation tables from this root to walk the dispatch journey.
- **(forthcoming) Reverse-call mechanics** — concretizes the parent-handler-context constraint with a real `tools/call → elicitation/create` example.
- **(forthcoming) Tasks subsystem (v1/v2/hybrid)** — long-running operations layered on top of correlation + notifications.
- **(forthcoming) SSE resumption** — `Last-Event-ID` replay, the server's event store, in-flight response recovery. (Leaf, not a root.)
