# SEP-2577 — Deprecation of Roots, Sampling, and Logging

[SEP-2577](https://github.com/modelcontextprotocol/specification/pull/2577) lands in the MCP 2026-07-28 RC (locked 2026-05-21). It puts three protocol features on a deprecation path: **Roots**, **Sampling**, and **Logging**. mcpkit is on the 12-month annotation-only path — every existing call still works at runtime; godoc `// Deprecated:` blocks fire `staticcheck SA1019` (and any IDE that consumes it) at call sites so consumers see the warning the moment they upgrade.

**Removal target:** mcpkit v0.4. v0.3.x (the 2026-07-28 RC line) keeps the surfaces fully functional alongside the deprecation comments. Until v0.4 cuts, *no behavior changes*.

## TL;DR

| Feature | mcpkit surface | Status in v0.3.x | What to do |
|---|---|---|---|
| Roots | `WithAllowedRoots`, `IsPathAllowed`, `RootsListResult`, `WithRootsHandler`, `NotifyRootsChanged` | works, deprecated | move root-list semantics into application-level resource conventions; see [Roots replacement](#roots) |
| Sampling | `Sample`, `CreateMessageRequest`, `SamplingMessage`, `ModelPreferences`, `WithSamplingHandler`, `TaskSample` | works, deprecated | bring your own LLM client; see [Sampling replacement](#sampling) |
| Logging | `EmitLog`, `LogLevel`, `LogMessage`, `MCPLogHandler` (slog adapter) | works, deprecated | use a standard structured-logging library; see [Logging replacement](#logging) |

## Why these three

The MCP working group's framing (paraphrased from SEP-2577 discussion): **Roots**, **Sampling**, and **Logging** all try to bridge between the MCP server and a host capability that has matured outside the protocol. Roots overlaps with the host's filesystem permission model; Sampling overlaps with the host's LLM-of-record; Logging duplicates structured-logging conventions every server runtime already has. Keeping them in the protocol surface forces every host implementer to either ship them or stub them. The deprecation removes that footgun.

## Timeline

| Date | Event |
|---|---|
| 2026-05-21 | SEP-2577 trigger fired (MCP RC lock) |
| 2026-05-31 | mcpkit annotation pass lands (this doc + `Deprecated:` blocks) |
| 2026-07-28 | MCP 2026-07-28 GA — features remain in the spec but flagged deprecated |
| 2027-05-21 | 12-month annotation window minimum closes |
| mcpkit v0.4 | mcpkit removes the deprecated symbols (no earlier than the spec window above) |

If the spec window extends, mcpkit's v0.4 cut follows it — the deprecation doc is the source of truth, not a calendar date.

## Affected symbols

### Roots

| Symbol | Replacement |
|---|---|
| `server.WithAllowedRoots(roots ...string)` | Application-level filesystem access control. mcpkit does not ship a drop-in. |
| `server.WithRootsFetchTimeout(d time.Duration)` | No replacement (Roots-specific). |
| `core.IsPathAllowed(ctx, path) bool` | Same — application-level check against your own allowlist. |
| `core.BaseContext.IsPathAllowed(path) bool` | Same. |
| `core.RootsListResult` | No replacement — the `roots/list` server-to-client request is the thing being deprecated. |
| `core.DecodeListRootsInputResponse` | MRTR helper for the deprecated `roots/list` flow; remove when the MRTR composition no longer needs roots input. |
| `client.RootsHandler`, `client.WithRootsHandler(h)` | Host wires filesystem permissions itself; mcpkit's client no longer needs to negotiate them after v0.4. |
| `(*client.Client).NotifyRootsChanged()` | No replacement — `roots/list_changed` is part of the deprecated surface. |

**Migration sketch:** Move from *"server asks client which paths are allowed"* (Roots) to *"application bakes in its own filesystem capability before construction."* For tools that legitimately need user-scoped file access, model that as a resource or as an explicit tool argument, not as a protocol-level negotiation.

### Sampling

| Symbol | Replacement |
|---|---|
| `core.Sample(ctx, req) (CreateMessageResult, error)` | Bring your own LLM client (`anthropic-sdk-go`, OpenAI SDK, etc.) and call it directly from the tool handler. |
| `core.BaseContext.Sample(req) (CreateMessageResult, error)` | Same — receive a model client via dependency injection at server construction. |
| `core.CreateMessageRequest`, `core.SamplingMessage`, `core.ModelPreferences`, `core.CreateMessageResult` | No replacement at the protocol surface — these wire types disappear. Application-level model abstractions replace them. |
| `core.NewSamplingInputRequest(req)`, `core.DecodeSamplingInputResponse` | MRTR helpers for the deprecated sampling-in-MRTR flow. |
| `(*server.TaskContext).TaskSample(req) (CreateMessageResult, error)` | Same as `Sample()` — task continuations should hold their own LLM client. |
| `client.SamplingHandler`, `client.WithSamplingHandler(h)` | After v0.4, the host's LLM client and the MCP client are separately wired — no protocol negotiation. |

**Migration sketch:** Most tools that previously did `ctx.Sample(...)` were really asking *"call the host's LLM with this prompt."* Pass that LLM client into the server constructor; tools call it directly. mcpkit's role narrows to tool dispatch + transport; sampling stops being a wire-level concept.

### Logging

| Symbol | Replacement |
|---|---|
| `core.EmitLog(ctx, level, logger, data)` | Use `slog.InfoContext(ctx, ...)` (or equivalent) — write to your own log sink. |
| `core.BaseContext.EmitLog(level, logger, data)` | Same. |
| `core.LogLevel` and constants (`LogDebug`, `LogInfo`, `LogNotice`, `LogWarning`, `LogError`, `LogCritical`, `LogAlert`, `LogEmergency`) | `slog.Level` (or your library's equivalent). |
| `core.LogMessage` | Wire-level type for the deprecated `notifications/message`; no replacement at this surface. |
| `core.MCPLogHandler` (slog → MCP bridge) | Drop the bridge; route slog to stderr / file / aggregator as you would in any other Go service. |

**Migration sketch:** mcpkit's logging existed to bridge tool output back to the MCP client over the wire. After deprecation, the client is no longer responsible for surfacing server logs — operators read them where every other Go service's logs go. If you need observability inside a host UI, that's an application-level feature now, not a protocol one.

## Notes for mcpkit contributors

- `// Deprecated:` blocks are per Go convention: blank-line-separated paragraph starting with `Deprecated:`. `staticcheck -checks SA1019` enforces no internal call sites accidentally regress to *new* uses of the deprecated symbols — current call sites are grandfathered through v0.3.x but should be tracked so v0.4 removal is mechanical.
- Examples (`examples/mrtr`, `examples/apps/*`, `examples/stateless`, `examples/tasks`) still demo the deprecated surfaces — they're working illustrations of how the API behaves in v0.3.x. Each affected example carries a README banner pointing here. After v0.4 cut, the examples migrate or get retired with the symbols they demo.
- The deprecation surfaces are wire-level: this doc does **not** deprecate `slog` integration as a general pattern, only mcpkit's MCP-protocol bridge for it. Same with model-client wiring — only the protocol-mediated path is leaving.
