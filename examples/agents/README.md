# examples/agents

Agent-focused examples grouped together, sharing a common demo harness.

- **`agent-async`** — an agent managing async work (events + tasks) through chat.
- **`multi-agent`** — Phase 3 composition: sub-agents-as-tools + handoff.

## Running

Each example is deterministic offline (`just agent` / `make agent` — scripted
StubProviders, no LLM). To drive it with a live model, `just demo` / `make demo`
resolves the model and endpoint from `llm.json` (or a gitignored
`llm.local.json` override) when `MODEL` / `BASE_URL` aren't set.

## `llm.json` — providers, no secrets

`llm.json` lists named connections (local, cloud, a router) in the same shape as
the host `ConnectionsConfig`. It carries **only** endpoint + model + the *name*
of the env var holding the key (`apiKeyEnv`) — **never a key**, so it is safe to
commit. The active connection is a local model, so `just demo` works offline
against a running LM Studio / Ollama with nothing to configure. A model router
(OpenRouter, LiteLLM, a gateway) is just another connection — point `baseURL` at
it. For machine-specific overrides, copy it to `llm.local.json` (gitignored).
