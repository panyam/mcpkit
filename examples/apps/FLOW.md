# MCP Apps — Flow Reference

How the pieces fit together when an MCP Apps tool is invoked, what each
component does, and where mcpkit fits in. Specific to the workflows in
this repo (`demo-app`, `inspect-app`, `test-apps-playwright`).

## The cast of characters

| Piece | Who owns it | Language | Where it lives |
|---|---|---|---|
| **`basic-host`** | upstream | TypeScript | `github.com/modelcontextprotocol/ext-apps/examples/basic-host` |
| **example TS servers** (`basic-server-vanillajs`, `integration-server`, …) | upstream | TypeScript | `github.com/modelcontextprotocol/ext-apps/examples/<name>` |
| **App HTML bundle** (`dist/mcp-app.html`) | upstream | per-example bundle (React/Vue/Solid/Vanilla → JS) | built by `npm run build` in each example |
| **`@modelcontextprotocol/ext-apps` library** | upstream | TypeScript | npm package — provides `registerAppTool`, host helpers, bridge JS |
| **MCPJam Inspector** | third-party (mcpjam) | npm CLI | `npx @mcpjam/inspector@latest` |
| **mcpkit `core/server/client`** | us | Go | `core/`, `server/`, `client/` packages |
| **mcpkit `ext/ui`** | us | Go + a TS bridge JS | MCP Apps extension support in Go |
| **mcpkit-Go compat fixtures** | us | Go | `examples/apps/compat/<name>/` — drop-in replacements for upstream's TS servers |

**"Upstream"** here means [`github.com/modelcontextprotocol/ext-apps`](https://github.com/modelcontextprotocol/ext-apps) — the official MCP Apps repo, TypeScript across the board. It owns the spec, the bridge protocol, the canonical host (`basic-host`), the reference example servers, and the Playwright test suite. **mcpkit is not upstream.** mcpkit is a parallel Go implementation of the MCP protocol that aims to be wire-compatible with upstream's reference servers.

## The boot sequence

What happens when you click a tool in basic-host's UI:

```mermaid
sequenceDiagram
    participant Browser
    participant Host as basic-host<br/>(TS, :8080)
    participant Sandbox as sandbox iframe<br/>(TS, :8081)
    participant Server as MCP server (:3101)<br/>TS OR mcpkit-Go fixture
    participant App as App iframe<br/>(upstream's dist/mcp-app.html)
    participant Bridge as mcp-app-bridge.js<br/>(loaded inside App)

    Note over Browser: user opens http://localhost:8080
    Browser->>Host: GET /
    Host-->>Browser: harness HTML + JS
    Browser->>Host: pick server from dropdown
    Host->>Server: initialize
    Server-->>Host: capabilities, serverInfo
    Host->>Server: tools/list
    Server-->>Host: tools[] with _meta.ui.resourceUri

    Note over Browser,Host: --- user clicks a tool ---
    Browser->>Host: tool click (or auto-call via ?call=true)
    Host->>Server: tools/call
    Server-->>Host: content + structuredContent + _meta
    Note over Host: reads _meta.ui.resourceUri = "ui://..."<br/>fetches the App HTML
    Host->>Server: resources/read("ui://...")
    Server-->>Host: HTML (text/html;profile=mcp-app)
    Host->>Sandbox: render sandbox iframe (different origin :8081)
    Sandbox->>App: nest App iframe, load HTML
    App->>Bridge: <script src="mcp-app-bridge.js">
    Bridge->>Sandbox: ui/initialize (postMessage)
    Sandbox->>Host: relay ui/initialize
    Host-->>Sandbox: ui/initialize result (hostCapabilities, theme, ...)
    Sandbox-->>Bridge: relay
    Bridge-->>App: connect() resolves — App is ready

    Note over App,Bridge: --- runtime: App ↔ host ---
    App->>Bridge: mcp.sendMessage("hello")
    Bridge->>Sandbox: ui/message (postMessage)
    Sandbox->>Host: relay
    Host-->>Browser: [HOST] log: "Message from MCP App: hello"

    App->>Bridge: mcp.callTool({name: "get-time"})
    Bridge->>Sandbox: tools/call (postMessage)
    Sandbox->>Host: relay
    Host->>Server: tools/call (MCP/JSON-RPC)
    Server-->>Host: result
    Host-->>Sandbox: result
    Sandbox-->>Bridge: relay
    Bridge-->>App: resolve callTool promise
```

Read this top-to-bottom: the host loads the tool surface from the server, the user picks a tool, the host fetches the App HTML and renders it in a nested sandboxed iframe, the bridge JS inside the App establishes its postMessage channel, and from then on the App can talk back to the host via the bridge.

## What each piece does

### `basic-host` (upstream, TS, port `:8080`)

The canonical reference host implementation. Single-page TS/React app served on `:8080`. Job:

- Discover MCP servers via the `SERVERS` env var
- Run `initialize` + `tools/list` against each
- Render the tool dropdown and call UI
- When a tool returns `_meta.ui.resourceUri`, fetch the App HTML via `resources/read` and render it
- Manage the nested sandbox iframe (CSP, sandbox attributes, postMessage relay)
- Provide host-side handlers for bridge events (`sendMessage`, `sendLog`, `openLink`, etc.)

**We don't own basic-host.** When upstream's Playwright suite runs, it's driving basic-host's UI in a browser.

### The MCP server (port `:3101`)

This is the one slot in the flow that can be **either** upstream's TS **or** our mcpkit-Go.

| Workflow | Server is… | Owner |
|---|---|---|
| `make demo-app` | upstream's TS server (`basic-server-vanillajs/dist/index.js` etc.) | upstream |
| `make inspect-app` | upstream's TS server (same as demo-app) | upstream |
| `make test-apps-playwright[-docker]` | mcpkit-Go drop-in (`examples/apps/compat/<name>/main.go`) | us |

Either way the **wire surface is identical**: respond to MCP/JSON-RPC over Streamable HTTP, return tool definitions with `_meta.ui.resourceUri`, serve the App HTML when the host fetches the `ui://...` resource. The compat fixtures' whole purpose is to be indistinguishable from upstream's TS at the wire layer.

### The sandbox iframe (upstream, TS, port `:8081`)

A separate-origin iframe basic-host uses to isolate the App from the host page. Apps can't touch basic-host's DOM directly; they go through the sandbox via `postMessage`, which forwards to basic-host. The separation enforces:

- **CSP**: the sandbox origin has its own Content-Security-Policy built from `_meta.ui.csp`
- **Sandbox attribute**: the iframe `sandbox` attribute restricts the App's capabilities (no top navigation, no parent access, etc.) from `_meta.ui.permissions`
- **Origin isolation**: a malicious App can't escape into basic-host's origin

### The App iframe (HTML bundle, served by the MCP server)

What the user actually sees. Each upstream example builds its own `dist/mcp-app.html` — a single-file bundle (Vite + `vite-plugin-singlefile`) containing the App's UI code and the `mcp-app-bridge.js` library.

The compat fixtures **don't build their own App HTML**. They read upstream's `dist/mcp-app.html` from `$EXT_APPS_DIR` at startup and serve it byte-for-byte verbatim. That's the contract — pretend to be upstream's TS server, including identical HTML.

### The bridge (`mcp-app-bridge.js`, upstream)

The JavaScript layer the App uses to talk to the host. Lives **inside** the App iframe. Exposes a `mcp` object with methods:

| Bridge method | What it does | Where it terminates |
|---|---|---|
| `mcp.callTool({name, arguments})` | invoke a server tool | server (via MCP `tools/call`) |
| `mcp.readResource(uri)` | fetch a resource | server (via MCP `resources/read`) |
| `mcp.sendMessage(text)` | push a message to the host | host (no server round-trip) |
| `mcp.sendLog({level, message})` | push a log line | host |
| `mcp.openLink(url)` | request the host open a URL | host |
| `mcp.updateModelContext({content, structuredContent})` | push context the LLM should see | host (forwards to LLM in real deployments) |
| `mcp.requestDisplayMode("fullscreen")` | request a display mode change | host |
| `mcp.downloadFile({contents})` | trigger a file download | host |

Each call is a `postMessage(ui/<method>, params)` to the sandbox iframe, which relays to basic-host. basic-host either handles it locally (`sendMessage` → console log) or forwards it as an MCP/JSON-RPC call to the server (`callTool` → `tools/call`).

This bridge ships as part of `@modelcontextprotocol/ext-apps`. mcpkit also ships its own bridge at `ext/ui/assets/mcp-app-bridge.js` for non-compat mcpkit Apps you'd write in Go — but in the compat flow we serve upstream's HTML which embeds upstream's bridge.

## Where mcpkit fits

In the compat flow (the focus of `apps/compat/`), mcpkit replaces exactly **one** slot in the diagram: the MCP server. Everything else is upstream.

```
                ┌──────────────────────────────────────────┐
                │           OWNED BY UPSTREAM              │
                ├──────────────────────────────────────────┤
                │  basic-host ←→ sandbox ←→ App ←→ bridge  │
                └──────────────────────────────────────────┘
                                     ↑ MCP/JSON-RPC over Streamable HTTP
                                     ↓
                ┌──────────────────────────────────────────┐
                │  The ONE slot mcpkit replaces in compat: │
                │  the MCP server                          │
                │  (Go binary under examples/apps/compat/) │
                └──────────────────────────────────────────┘
```

mcpkit packages doing real work in this flow:

- **`core/`** — protocol types (`ToolDef`, `ToolMeta`, `UIMetadata`, `ToolExecution`, etc.). What gets serialized on the wire.
- **`server/`** — HTTP server, Streamable HTTP transport, JSON-RPC dispatch, session management.
- **`ext/ui/`** — MCP Apps extension support. `RegisterTypedAppTool`, the `_meta.ui` shape, the dual `ui.resourceUri` / `ui/resourceUri` key emit, the `UIExtension` extension marker.
- **`examples/common/MCPServerOptions(...)`** — boilerplate for spinning up a mcpkit server with sensible defaults.

Outside compat (a "normal" mcpkit App you'd ship in production), mcpkit additionally provides:

- **`ext/ui/assets/mcp-app-bridge.js`** — our own bridge JS for Apps you author in Go. Spec-compatible with upstream's bridge; you'd embed it in your own App HTML.
- **`ext/ui/AppHost`, `ServerRegistry`** — if you want a Go program to act AS an Apps host (not the test scenario; relevant for headless agent harnesses).

## The three workflows in this repo

| Command | What runs | What you're testing |
|---|---|---|
| `make demo-app EXAMPLE=<name>` | upstream's TS server + basic-host | "Show me what this App looks like, rendered." Browses upstream's reference behavior. |
| `make inspect-app EXAMPLE=<name>` | upstream's TS server + MCPJam Inspector (`npx`) | "Show me the protocol surface — tools/list JSON, `_meta.ui`, tool-call payloads." Browses the wire. |
| `make test-apps-playwright[-docker] EXAMPLE=<name>` | **mcpkit-Go fixture** + basic-host + Playwright | "Does mcpkit's Go drop-in match upstream's TS, byte-for-byte at the wire AND visually after rendering?" Tests our parity. |

The first two use upstream's TS servers (to see / inspect upstream's reference). The third is the only one that exercises mcpkit's Go server. The strict `tools/list` parity check in DOCKER mode compares our Go fixture's wire output against upstream's TS, both running side-by-side.

## On the `ui://` URI scheme

`ui://get-time/mcp-app.html` is a regular MCP resource URI — basic-host fetches it via `resources/read` like any other resource. The `ui://` prefix is a **convention** from the MCP Apps spec that signals "this resource serves App HTML with MIME `text/html;profile=mcp-app`." Nothing in the protocol parses the URI; it's just a string identifier. The same App resource could equally be served at `https://...` or `resource://...` — the prefix exists to make Apps-vs-non-Apps resources easy to distinguish at a glance.

## On `_meta.ui` (and the dual `ui/resourceUri` key)

Tool definitions can carry MCP Apps metadata under `_meta`. The canonical form is nested:

```json
"_meta": {
  "ui": {
    "resourceUri": "ui://get-time/mcp-app.html",
    "visibility": ["model", "app"]
  }
}
```

Older clients may read a flat fallback key `"ui/resourceUri"` instead. Upstream's `registerAppTool` emits **both** the nested form and the flat fallback for backward-compat — mcpkit's `core.ToolMeta` does the same via custom `MarshalJSON`/`UnmarshalJSON`. Either is valid input; both are emitted on output.

## Production hosts vs `basic-host`

`basic-host` is a **reference** implementation for testing and demos. Real-world MCP Apps hosts (Claude.ai, ChatGPT, Cursor, etc.) implement the same protocol but with their own UI, their own sandbox enforcement, their own LLM integration. The flow above is identical at the protocol layer — only the host's outer chrome differs.

Any App that works in `basic-host` should work in a production host, modulo host-specific feature support (display modes, permissions, etc. that the App declares via `_meta.ui.*`).
