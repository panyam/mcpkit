# agentchat playground

One command to feel the mcpkit agent SDK: `just pg` from the repo root boots a
demo MCP server (`examples/getting-started/server`, a `greet` tool on
`:8787`) and launches `agentchat` in its TUI, wired to a local model, a
SQLite session store, and a filesystem offload dir.

```bash
just pg
```

## What you need

A local OpenAI-compatible model endpoint. The bundled `playground.json`
assumes [LM Studio](https://lmstudio.ai) on its default `http://localhost:1234/v1`
with a model named `qwen2.5-7b-instruct` loaded. Edit `connections` in
`playground.json` to match what you have (or point `type`/`baseUrl` at Ollama,
vLLM, or any OpenAI-compatible server). `/provider` in the TUI switches between
the configured connections at runtime.

## What to try in the TUI

- Type a message. Ask it to `greet` someone — watch the live tool call.
- `↑` / `↓` recall previous inputs; the input box is editable and cursor-traversable.
- `/` then `Tab` completes slash commands; `/provider `<Tab> cycles connections,
  `/sessions `<Tab> cycles saved sessions.
- `/fork` branches the conversation; `/sessions` lists saved sessions and switches;
  `/resume <id>` reopens one. Sessions persist to the SQLite file, so they survive
  restarts.
- Large tool outputs are offloaded to `~/.agentchat/pg-blobs/` and replaced by a
  stub the model can query with `read_tool_result`.

`--ui plain` (or piping) falls back to the line-based REPL.
