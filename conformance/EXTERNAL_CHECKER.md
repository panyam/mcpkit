# External Stateless-Draft Conformance

Snapshot of the mcpkit **client** graded by the third-party stateless-draft gauntlet at
[`https://mcp-checker-2026-07-28.val.run`](https://mcp-checker-2026-07-28.val.run) — an MCP server that judges every request its *client* sends.

**Protocol version:** `2026-07-28` / `DRAFT-2026-v1` (this checker grades that version only)  
**Surface under test:** the mcpkit **client** (`client.NewClient(..., WithClientMode(ClientModeStateless))`)  
**Driver:** `cmd/external-checker`  
**Grader:** `mcp-checker-2026-07-28` v1.0.0

**Verdict:** **PASS** — 5 / 5 checks passed.

Unlike every `testconf-*` suite — which grades an mcpkit *server* with the upstream
runner acting as the client — this report inverts the roles: a real mcpkit client is
the thing under test and the remote endpoint is the grader. It is the only check that
exercises the **integrated** stateless draft wire (SEP-2575 + 2243 + 2106 + 2322 enforced
simultaneously, by an independent third party) from the client side.

The endpoint is an external, version-pinned, ephemeral deployment, so this is a
**point-in-time snapshot**, not a CI gate. Regenerate via `make testconf-external-checker`.

## Results

| Check | SEP | Result | Detail |
|---|---|---|---|
| Connect (stateless wire) | SEP-2575 | ✅ pass | no initialize handshake; MCP-Protocol-Version header + _meta envelope accepted on every POST |
| clientCapabilities in _meta | SEP-2575 | ✅ pass | tools/list returned mrtr_confirm, validate_arguments |
| validate_arguments | SEP-2106 | ✅ pass | CONFORMANCE OK [validate_arguments]: message=hello-from-mcpkit, count=42, payload.kind=solid |
| mrtr_confirm (MRTR round-trip) | SEP-2322 | ✅ pass | CONFORMANCE OK [mrtr_confirm]: requestState echoed intact; elicitation response valid (action=accept) |
| Routing headers (Mcp-Method/Mcp-Name) | SEP-2243 | ✅ pass | every graded request was accepted — a missing routing header is rejected before tool dispatch |

## What each SEP requires of the client

- **SEP-2575 (stateless wire):** `MCP-Protocol-Version` header on every POST; `_meta` carries
  `io.modelcontextprotocol/protocolVersion`, `clientInfo`, and `clientCapabilities`; no `initialize` handshake.
- **SEP-2243 (routing headers):** `Mcp-Method` mirrors the JSON-RPC method on every POST; `Mcp-Name` on tool calls.
- **SEP-2106 ($ref arguments):** arguments honor the `inputSchema` — real JSON numbers (not stringified) and
  same-document `$ref` payloads resolved by the caller.
- **SEP-2322 (MRTR):** handle a `resultType: input_required` result, answer the elicitation with `{action, content}`,
  and re-call echoing `requestState` back unchanged.

## Known caveat

`ClientModeStateless`'s `Connect()` treats `server/discover` as **mandatory** and fails fatally if the
server omits it (`client/client.go`). This gauntlet implements `server/discover`, so the check passes — but
the draft states a client may "start with `server/discover` *or any request directly*." Against a conformant
draft server that omits discover, mcpkit's stateless client cannot currently connect. Tracked as
issue 829; it does not affect the grade above.
