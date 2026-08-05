# deep-agent-supervisor

mcpkit's host acting as a **deep-agent supervisor** over a **server-advertised
agent roster**. A connected MCP server declares a small roster of specialist
agents through the `experimental/ext/agents` discovery primitive (epic 1142);
the supervisor sees only routing tuples, delegates by natural-language match,
and each specialist scopes its own tools.

This is the demo the WG stress test in `agents-wg#20`
(`docs/research/deep_agent_mcp_analysis.md`) benchmarks against LangChain Deep
Agents. It is issue 1146; the WG post is gated on it and is not part of this
example.

## What it shows

- **Progressive disclosure.** The supervisor's context holds three short agent
  tuples (`research`, `workflow`, `insights`), NOT every specialist's tool
  schemas. `agents/list` returns descriptions; `agents/get` resolves a
  specialist's instructions + scoped tools only when the supervisor decides to
  delegate.
- **Server-advertised specialists, not host-declared personas.** The config has
  no `subAgents` block. The host discovers the roster from the server
  (`agents/list`) and exposes each agent as a delegate tool automatically
  (`ServerConfig.agents`, on by default).
- **Scoped execution loops back to the server.** A resolved specialist runs a
  child `Runner` whose tool calls dispatch back to the advertising server via
  `tools/call`. A specialist can call only the tools its `agents/get` definition
  scoped it to, never the server's full tool set.
- **Observability end to end.** With an exporter set, one delegation produces a
  trace of `supervisor turn -> agents.resolve(agent.id) -> agents.get -> child
  turn` (issue 1145).

### Roster (maps to `agents-wg#20` §6)

| Agent | Role | Scoped tools |
|---|---|---|
| `research` | deep-research specialist (the lead) | `web_search`, `fetch_page`, `summarize` |
| `workflow` | CI/CD operations specialist | `list_pipelines`, `run_pipeline`, `pipeline_status` |
| `insights` | analytics specialist | `query_metrics`, `detect_anomaly` |

The specialists' tools are deterministic stubs, so the demo runs without any
external service. Only the supervisor's own model needs a provider.

## Run

Two terminals:

```bash
# Terminal 1 — the roster server on :8795
just serve

# Terminal 2 — the supervisor host
just demo
```

The supervisor's model comes from `deep-agent-supervisor.json` (active
`deepseek-r1`, DeepSeek's `deepseek-reasoner`, keyed on `DEEPSEEK_API_KEY`).
Override it with any connection in the config:

```bash
ACTIVE=anthropic-sonnet just demo      # or openai-5.1, gemini-3-pro, local, ...
```

`local` (an LM Studio endpoint) is included for an offline run — load a model
in LM Studio first, then `ACTIVE=local just demo`.

Then ask things that route to different specialists, e.g.:

- "Research the tradeoffs of server-declared subagents vs host-declared personas."
- "Which pipelines are failing, and why?"
- "Any anomalies in error rate today?"

### Traces

Set an exporter on both sides to emit the discovery + delegation spans:

```bash
EXPORTER=otlp just serve      # Terminal 1
EXPORTER=otlp just demo       # Terminal 2
```

`docker/observability` (Tempo/Grafana) is one place to view them. The server
emits `agents.list` / `agents.get`; the host emits `agents.resolve` and the
child specialist's `agent.turn` / `agent.step` / `agent.tool` spans, stitched
into one trace by W3C trace-context propagation.

## Layout

```
deep-agent-supervisor/
├── deep-agent-supervisor.json   # agentchat config (one server, no subAgents)
├── run.sh / justfile            # launch the supervisor / the server
└── server/                      # the roster MCP server (own go.mod)
    ├── main.go                  # buildServer(): tools + agents.Register roster
    └── main_test.go             # roster wiring: list=3 tuples, get=scoped tools
```
