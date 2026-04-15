# Spec gaps: pre-fetch timing, template URI substitution, and resource notifications for apps

**Repo:** modelcontextprotocol/ext-apps

## Context

While implementing MCP Apps support in [mcpkit](https://github.com/panyam/mcpkit) (Go library) and testing with MCPJam, we discovered several behaviors that are not covered by the current spec. These affect server implementors who use template URIs in `_meta.ui.resourceUri`.

## Observations

### 1. Pre-fetch timing is unspecified

Hosts pre-fetch `resources/read` for `_meta.ui.resourceUri` **before** `tools/call` completes (MCPJam fetches ~300ms before the tool result arrives). The spec does not define when hosts should fetch the resource relative to the tool call.

**Impact:** Servers using template URIs (e.g., `ui://app/items/{id}/view`) cannot populate the resource content until the tool is called and the `{id}` parameter is known. The pre-fetch arrives with no context about which item to render.

**Workaround:** We generate a concrete fallback URI (e.g., `ui://app/preview_item/latest`) and return a placeholder HTML document on pre-fetch. After the tool call, the real content is available on subsequent fetches.

### 2. Template variable substitution in `_meta.ui.resourceUri`

The spec does not say whether hosts should substitute template variables in `_meta.ui.resourceUri` before calling `resources/read`. Current hosts (MCPJam, Claude Desktop, VS Code Copilot) fetch the URI **literally** without substitution.

**Impact:** A tool with `_meta.ui.resourceUri: "ui://app/items/{id}/view"` results in the host fetching that exact string as a resource URI, which won't match any concrete resource.

**Suggestion:** Either:
- (a) Specify that hosts SHOULD substitute template variables from tool arguments before fetching, or
- (b) Explicitly state that `_meta.ui.resourceUri` MUST be a concrete URI, and document the fallback pattern for servers that want parameterized resources

### 3. Role of `notifications/resources/updated` for app resources

After a tool call updates the underlying data, the server emits `notifications/resources/updated` for the app resource URI. However, MCPJam does not re-fetch the resource in response — tool results are delivered to the iframe via `postMessage` (`ui/notifications/tool-result`) instead.

**Question:** Is `notifications/resources/updated` expected to trigger a resource re-fetch for app resources, or is the `postMessage` channel the intended update path? If the latter, this should be documented so servers don't rely on resource notifications to update app state.

### 4. `structuredContent` type constraint

The draft schema defines `structuredContent` as `"type": "object"`, but this is easy to miss. MCPJam validates this and rejects arrays with `"expected record, received array"`.

**Suggestion:** The spec prose should explicitly note that `structuredContent` must be a JSON object (not an array or primitive), since the schema constraint alone is easy to overlook.

## Environment

- Server: mcpkit v0.2.13 + slyds (Go, Streamable HTTP)
- Host: MCPJam (April 2026)
- Spec version: 2026-01-26
