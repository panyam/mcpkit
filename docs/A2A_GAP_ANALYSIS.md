# A2A vs mcpkit: gaps, overlaps, and what a bridge would look like

Status: analysis, 2026-09-24. Nothing here is implemented. Written to decide whether mcpkit should
grow primitives that bridge to Agent2Agent (A2A), and if so which ones.

Baselines compared:

| Surface | Version read |
|---|---|
| A2A | **v1.0.1** (2026-05-26), `a2aproject/A2A` main `43e0c874`; `specification/a2a.proto` is normative since v1.0. Hosted by AAIF alongside MCP since 2026-08-17. |
| MCP spec | **2026-07-28** (current GA, stateless wire, tasks moved to an extension). `draft/` has no content yet. |
| MCP pipeline | Open SEP PRs as of 2026-09-24, the roadmap of 2026-08-22, Agents WG charter (2026-08-04), `experimental-ext-*` repos. |
| mcpkit | this tree: `ext/tasks` (SEP-2663), MRTR, `server/stateless`, `experimental/ext/events`, `experimental/ext/agents`, `ext/skills`, `ext/auth`. |

## 1. The one-line framing

MCP is **tool-shaped**: a host calls a typed operation with a JSON Schema and gets a typed result.
A2A is **conversation-shaped**: a client sends a *message* made of parts to an opaque agent, and the
agent decides whether to answer with a message or open a *task* that lives inside a *context*,
produces *artifacts*, and can stop to ask for input or auth.

Since 2026-07-28 the two are closer than they have ever been. MCP is now stateless per request and has
an async task handle (SEP-2663). Its input rounds are typed (MRTR), its webhooks are signed
(triggers-events), and its extensions are URI-keyed (SEP-2133). Most of the remaining gap is
**conversation structure and output structure**. Transport and lifecycle are mostly closed.

## 2. Overlaps (both protocols have it; mapping is mechanical)

| Concept | A2A v1.0 | MCP / mcpkit | Fit |
|---|---|---|---|
| Async unit of work | `Task` (server-minted id) | SEP-2663 task handle, `ext/tasks` | Good |
| Working / done states | `WORKING`, `COMPLETED`, `FAILED`, `CANCELED` | `working`, `completed`, `failed`, `cancelled` | 1:1 |
| Needs input | `INPUT_REQUIRED`; the client replies with a new Message on the same `taskId` | `input_required` with typed `inputRequests`; the client answers with `tasks/update` | Semantic fit, shape differs (free text vs typed elicitation) |
| Get / cancel | `GetTask`, `CancelTask` (idempotent) | `tasks/get`, `tasks/cancel` | 1:1 |
| Webhook push | `TaskPushNotificationConfig` per task, `Authorization: scheme creds`, SSRF guidance | `events/subscribe`: Standard Webhooks signing, `whsec_` secret, endpoint verification, SSRF guard, TTL leases | mcpkit's machinery is **stronger**, but it is attached to event sources, not tasks |
| Capability discovery | AgentCard at `/.well-known/agent-card.json` | `server/discover` (SEP-2575); Server Card (SEP-2127, in review) | Partial; see §3 |
| Skills catalogue | `AgentSkill{id,name,description,tags,examples,in/outModes}` | `experimental/ext/agents` `AgentSummary{agentId,description,capabilities,exampleTasks,delegateTool}` | Close in shape. **Not** SEP-2640 skills, which are instruction documents. The word collides. |
| Extensions | `capabilities.extensions[{uri,required,params}]`, activated per request via `A2A-Extensions` | `capabilities.extensions[id]` (SEP-2133); per-request client caps in `_meta` | Close; A2A has `required` + `ExtensionSupportRequiredError`, MCP does not |
| Auth | `securitySchemes` on the card (OAuth2 incl. device code, OIDC, mTLS, API key, HTTP) | PRM (RFC 9728) + AS metadata, CIMD, client credentials, ID-JAG in `ext/auth` | Both OAuth-centred; A2A declares inline, MCP discovers |
| Tracing | Traceability extension (sample) | SEP-414 `_meta.traceparent`, `ext/otel` | MCP ahead |
| Stateless HTTP | Always | 2026-07-28 | Converged |

## 3. What A2A does that mcpkit does not

Each gap is checked against the MCP pipeline. The last column decides whether mcpkit should build
anything. **"Pipeline"** means an MCP SEP or WG already owns it: track it, do not invent a wire for it.

| # | A2A feature | mcpkit today | MCP pipeline | Verdict |
|---|---|---|---|---|
| G1 | **`contextId`**: groups many tasks and messages into one conversation. Server-minted, mismatches rejected. Plus **`referenceTaskIds`** | Nothing. SEP-2567 moved cross-call state into tool-minted handles | Agents WG lists "multi-turn interaction" as in scope, with no work item | **Real gap, unowned** |
| G2 | **Message-in, agent-decides-out**: `SendMessage` returns a `Message` or a `Task` | `tools/call` with a schema. `experimental/ext/agents` routes to a `delegateTool` | Agents WG "Agents Extension evaluation", in progress, no SEP | Pipeline (unsettled) |
| G3 | **Artifacts**: named, multi-part outputs separate from chat messages, **streamed incrementally** (`TaskArtifactUpdateEvent{append,lastChunk}`) | `content[]` + `structuredContent`; the non-standard `notifications/tools/content_chunk` | SEP-2694 resumable task event streams (no sponsor, dormant); roadmap item "results that stream"; tool-result redesign | Partial pipeline; **mcpkit-local work possible** |
| G4 | **`SubscribeToTask`**: ordered event stream, current Task first, many concurrent subscribers | `notifications/tasks` push plus `tasks/get` polling | SEP-2694 (dormant); roadmap composition review of Tasks/`subscriptions/listen`/Triggers | Pipeline |
| G5 | **`ListTasks`** with filters (`contextId`, status, timestamp), authorized scope, pagination | v1 has `tasks/list`; **SEP-2663 removed it**; `TaskStore` is in-memory only | Removed on purpose | Wire: no. **Store API: yes**, a bridge needs it |
| G6 | **Per-task push config CRUD** | Webhook engine exists in `events`, not wired to tasks | Agents WG says it owns "webhook-style task completion notifications"; no SEP | Pipeline for the wire; **the engine is reusable now** |
| G7 | **`SUBMITTED`, `REJECTED`, `AUTH_REQUIRED`** states | None of the three | `AUTH_REQUIRED` ≈ SEP-2848 async approval (proposal, unsponsored) + URL-mode elicitation. `REJECTED`/`SUBMITTED`: nothing | Mapping only |
| G8 | **Content-mode negotiation**: `default{Input,Output}Modes`, per-skill modes, `acceptedOutputModes`, `ContentTypeNotSupportedError` | None | None | **Real gap, unowned**; small |
| G9 | **Signed cards**: JWS over RFC 8785 JCS-canonical card, multiple sigs for rotation | None | SEP-2127 server card has no signing; SEP-2752 (HTTP message signing) and SEP-2809 (attested admission) are adjacent | **Real gap, unowned** |
| G10 | **Extended (authenticated) card** | `server/discover` returns the same thing to everyone | None | Small; bridge-level |
| G11 | **Multi-tenancy**: `tenant` on every request and on `AgentInterface` | `ext/auth` TenantMapper (auth-side only) | None | Bridge-level |
| G12 | **gRPC and HTTP+JSON bindings**, functionally equivalent | JSON-RPC over HTTP/stdio only (protogen maps gRPC *services* to tools) | SEP-2598 pluggable transports (deferred) | Out of scope for MCP; only a bridge would speak them |
| G13 | **`required` extensions** + `ExtensionSupportRequiredError`; echo of activated extensions | Per-extension ad-hoc gating (tasks v2 checks client caps) | None | Minor |
| G14 | **Message history** and `historyLength` | None (MCP is not a transcript protocol) | None | Only inside a bridge; do not add to MCP |

In the other direction, mcpkit has things **A2A lacks** that a bridge should keep rather than lose:
- **typed** input requests (elicitation schemas, sampling) instead of free-text `INPUT_REQUIRED`
- HMAC-signed stateless round state (MRTR)
- **normative** webhook signing and endpoint verification (A2A leaves JWT/JWKS non-normative)
- JSON Schema on every operation
- SEP-414 tracing
- progressive disclosure of agent tool schemas (`agents/list` → `agents/get`)

## 4. Should mcpkit bridge the gaps?

**Yes, as a bridge and as protocol-neutral primitives. No, as new MCP wire.** Four constraints from
this repo's own history decide the shape:

1. **Do not squat on the MCP namespace.** `experimental/ext/agents/NOTES.md` already argues this.
   Matching an official name before the shape settles fails silently. G2, G4 and G6 belong to the
   Agents WG and the triggers-events work. mcpkit should *feed* them, not pre-empt them.
2. **The agent layer lives in chakra.** An A2A *agent executor* (LLM loop, history, planning) is
   agent-layer and belongs there. mcpkit owns wire, lifecycle, storage seams, and delivery.
3. **Dependencies run one way and heavy ones sit in sub-modules.** `a2a-go` pulls in gRPC and
   protobuf. It must live in its own `go.mod`, never under `core/`.
4. **Follow the document.** A2A went 0.3 → 1.0 with a full rename (PascalCase methods,
   SCREAMING_SNAKE enums, unified `Part`). A bridge pins a spec version and says so, as
   `testconf-events` does.

The case *for* doing it: MCP and A2A now sit side by side in the same foundation. Hosts that speak MCP
want to call A2A agents, and teams with an MCP server want it discoverable as an A2A agent. mcpkit
already holds most of the hard parts: a task state machine, MRTR, signed webhooks, auth, and stateless
wire. So a bridge is mostly **mapping**, and very little of it is new machinery.

## 5. Short term (primitives useful to MCP whether or not A2A ships)

Ordered by value over risk. Each item stands alone.

**S1. Durable, listable task store.**
- Add Redis/GORM `TaskStore` implementations beside the events stores. Extend the Go-level
  interface with filtered listing (principal, status, updated-after, an opaque `groupKey`).
- No wire change: SEP-2663 still has no `tasks/list`.
- Why: production tasks v2 needs durability anyway (SEP-2663 says clients SHOULD persist task IDs
  across crashes, which is pointless if the server forgets them). A2A `ListTasks` and G1 context
  grouping both reduce to this.

**S2. Lift the webhook engine out of `events`.**
- `experimental/ext/events` already has Standard Webhooks signing, `whsec_` secrets, endpoint
  verification, the SSRF guard, retries and TTL leases.
- Extract that into a delivery package, say `ext/webhooks`, with the event-source binding kept in
  `events`. Then the same deliverer can serve:
  - per-task completion webhooks under a vendor id (`io.mcpkit/task-webhooks`) until the Agents WG
    specifies one (G6);
  - A2A push notifications, which are a strictly weaker profile of the same thing.

**S3. `resource_link` content.**
- `core.Content` has no `uri`/`name` fields, so mcpkit cannot emit or faithfully decode the spec's
  `resource_link` (in MCP since 2025-06-18). This is an MCP gap before it is an A2A one.
- A2A `Part{url, mediaType, filename}` and file artifacts map onto it directly (G3). Fix it on its
  own merits.

**S4. Typed task progress / artifact updates as a vendor extension.**
- Promote `notifications/tools/content_chunk` into a task-scoped
  `notifications/tasks/artifact` (vendor id). Shape: `{taskId, artifactId, name, content[], append,
  lastChunk}`.
- Keep it deliberately A2A-shaped so the bridge is a rename, and ready to fold into SEP-2694 or
  whatever the "results that stream" roadmap item becomes.
- This is the one short-term item that *is* new wire, so it stays experimental and vendor-prefixed.

**S5. Server Card (SEP-2127) under `experimental/ext/servercard`, with optional JWS signing.**
- Track `experimental-ext-server-card` (`GET <endpoint>/server-card`, AI Catalog at
  `/.well-known/ai-catalog.json`).
- Put signing behind a vendor field using A2A's exact recipe (JWS over an RFC 8785 JCS card, multiple
  signatures for rotation). One canonicalizer + signer then serves both cards (G9).
- The AI Catalog is explicitly cross-protocol (MCP and A2A), which is where the two discovery
  stories meet.

**S6. `experimental/ext/a2a` sub-module (own `go.mod`, depends on `a2a-go`), two adapters.**

*Outbound: A2A agent → MCP tools* (highest value; lets any MCP host call A2A agents)
- Fetch and verify the AgentCard. Expose each `AgentSkill` as a tool: `description` + `examples`
  become the description, and the input schema is `{message: string, parts?: [...], contextId?:
  string}`.
- `tools/call` → `SendMessage` (`returnImmediately` when the client declared tasks v2).
- A2A `Message` reply → sync `ToolResult`. A2A `Task` → `GoAsyncResult` → SEP-2663 task.
- `INPUT_REQUIRED` → an elicitation `inputRequest` carrying the agent's status message; the answer
  goes back as the next `SendMessage` on the same `taskId`.
- `AUTH_REQUIRED` → URL-mode elicitation where the agent supplies a URL; otherwise a task failure
  with a clear error.
- Artifacts → `content[]` (text/data inline, `url` parts as `resource_link`), plus `structuredContent`
  for `data` parts.
- `contextId` returned as a SEP-2567 handle in `structuredContent`, so the model threads it through
  the next call (G1 solved *in the bridge*, not the wire).

*Inbound: MCP server → A2A agent* (lets an mcpkit server be discovered and called as an A2A agent)
- Serve `/.well-known/agent-card.json`, built from the `experimental/ext/agents` roster
  (`AgentSummary` → `AgentSkill`) or, lacking one, from `tools/list`.
- `SendMessage` → the skill's `delegateTool`. Tasks back onto the same S1 store, and push config backs
  onto S2.
- JSON-RPC binding first. gRPC/REST come free from `a2a-go` if wanted.

State mapping used by both directions:

| A2A | SEP-2663 |
|---|---|
| `SUBMITTED`, `WORKING` | `working` |
| `INPUT_REQUIRED` | `input_required` (elicitation) |
| `AUTH_REQUIRED` | `input_required` (URL-mode elicitation) |
| `COMPLETED` | `completed` |
| `FAILED` | `failed` |
| `REJECTED` | `failed`, with `error.data.a2aState="REJECTED"` |
| `CANCELED` | `cancelled` |

Test it the way this repo tests everything: a `testconf-a2a` stage driving the bridge with the
A2A TCK (`a2aproject/a2a-tck`), plus a break-the-fixture check that the suite goes red.

## 6. Strategic

**T1. One task core, two front-ends.**
- mcpkit's `ext/tasks` runtime is already most of a wire-agnostic task engine. Its pieces are the
  state machine, store, input-round waiters, cancellation, detach-for-background and tracing.
- Make that explicit, then let SEP-2663 and A2A each be a thin codec over it. With it a Go service
  implements one handler and answers both protocols from one task table.
- Neither official Go SDK can offer this, because each owns only one protocol. It is the most
  defensible reason for mcpkit to do A2A at all.

**T2. Feed the Agents WG with running code.**
- The WG's open items are "Agents Extension evaluation" and "two-level agent definition PoC".
  `experimental/ext/agents` *is* a two-level definition. The bridge would show exactly where MCP's
  agent shape falls short of A2A.
- The concrete proposals it would evidence:
  - a conversation/context grouping key on tasks (G1)
  - a subscribe-to-task stream (G4, reviving SEP-2694 with a sponsor)
  - per-task webhooks built on triggers-events signing (G6)
  - an auth-pending task state (G7, via SEP-2848)
- This is the Events story again. Building against a document found thirteen divergences, and
  asking the author settled the capability placement.

**T3. Push signing and content modes into Server Card.**
- Two A2A features have no MCP owner and are cheap to spec: signed cards (G9) and output-mode
  negotiation (G8).
- The Server Card WG and the cross-protocol AI Catalog are the natural home. Offer S5's signer as the
  reference.

**T4. Stay a library, not a gateway.**
- `docs/GATEWAY_DESIGN.md` already cites IBM ContextForge (federated MCP/A2A proxy) as prior art.
- mcpkit should supply the adapters and let gateways, chakra among them, compose them. It should not
  grow registry or federation features itself.

## 7. What not to do

- Do not add `contextId`, `Message`, `Part`, or history to `core/` types. They are A2A's model,
  and MCP's answer to cross-call state is handles (SEP-2567).
- Do not add a gRPC or REST MCP transport to reach A2A parity. That is SEP-2598's call, and it is
  deferred.
- Do not re-add `tasks/list` to the v2 wire. SEP-2663 removed it on purpose; the store API (S1) is
  enough.
- Do not declare anything under `io.modelcontextprotocol/*` for this work.
- Do not put an A2A executor or LLM loop here. That is chakra.

## 8. Open questions before building

1. Is the outbound adapter (S6a) enough on its own for the first cut? It has the clearest users
   and needs no inbound HTTP surface.
2. Should `ext/webhooks` (S2) be promoted out of `experimental/`, given that `events` itself is
   still experimental?
3. Which A2A version should the bridge pin: v1.0 only, or also v0.3 through a second
   `AgentInterface` the way A2A cards themselves do?
4. Should the outbound adapter's `contextId` be a model-visible handle, or a host-side detail kept
   in `_meta`? That choice decides whether the model can branch conversations.

## Sources

- A2A: `a2aproject/A2A` (`specification/a2a.proto`, `docs/specification.md`, `docs/whats-new-v1.md`,
  `CHANGELOG.md`, `docs/roadmap.md`, `docs/topics/extensions.md`); https://a2a-protocol.org/latest/specification;
  AAIF announcement https://aaif.io/blog/a2a-joins-aaif
- MCP: https://modelcontextprotocol.io/specification/2026-07-28/changelog;
  https://modelcontextprotocol.io/development/roadmap; Agents WG charter
  https://modelcontextprotocol.io/community/working-groups/agents; SEP PRs 2127, 1932, 1933, 2694, 2848;
  `modelcontextprotocol/experimental-ext-server-card`, `experimental-ext-triggers-events`
- mcpkit: `ext/tasks`, `core/task_v2.go`, `core/tool.go` (`Content`), `experimental/ext/events`,
  `experimental/ext/agents/{README,NOTES}.md`, `ROADMAP.md`, `docs/GATEWAY_DESIGN.md`
