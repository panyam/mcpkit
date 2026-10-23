# events bridge

> **EXPERIMENTAL**, like the rest of `experimental/ext/events`.

Lets a user say "when X happens, do Y" in an MCP host and have it happen later with no chat session open. The bridge is an MCP server facing the host and an [MCP Events](../../../README.md) client of one or more upstream events servers. It subscribes by webhook, with itself as the receiver, and hands each event to a sink. The shipped sink starts a headless `claude -p` run.

```
host (Claude Code) --MCP--> bridge --events/subscribe--> events server
                                  <--webhook POST------
                            bridge --claude -p--> headless run
```

## Tools

Four tools cover every event type on every configured server.

| Tool | Does |
|------|------|
| `watch_list(server?, query?)` | Runs `events/list` and returns qualified names (`<server>/<event>`), each with its description and `inputSchema`. `query` keeps only events whose name or description contains every word, so a large catalog stays out of context. Sorted by qualified name. |
| `watch(event, arguments, instruction)` | Checks `arguments` against the event's `inputSchema`, subscribes, and stores `instruction`. A mistake comes back as a tool error that says what to fix. |
| `unwatch(id)` | Stops the watch and unsubscribes it upstream. |
| `watches()` | Every standing watch with its status and last error, sorted by id. |

The tools never show a watch's webhook secret.

## Running it

```bash
go run ./cmd/events-bridge \
  -upstream ops=http://localhost:8080/mcp \
  -public-url http://localhost:8790 \
  -state ~/.config/events-bridge/watches.json \
  -allowed-tools Read,Grep,Glob

claude mcp add --transport http events-bridge http://localhost:8790/mcp
```

`-upstream name=url` repeats, one per events server, and the name becomes the prefix in qualified names. A bearer token for an upstream comes from `EVENTS_BRIDGE_TOKEN_<NAME>`. `-public-url` is where the upstream servers can reach the bridge's `/hooks/` path. A server with SSRF protection on refuses a loopback URL unless it is configured to allow one.

### Demo against kitchen-sink

```bash
# terminal 1: an events server whose alert.fired fires every second
cd examples/events/kitchen-sink && go run . --serve -addr :8080 -alert-every 1s

# terminal 2: the bridge, printing each run's prompt instead of starting claude
cd experimental/ext/events/clients/go && go run ./cmd/events-bridge -upstream ops=http://localhost:8080/mcp -dry-run
```

Then, in a Claude Code session with the bridge added: "when a P1 alert fires, summarize it in one line". The model calls `watch_list` with `query: "alert"`, then `watch` with `ops/alert.fired`, `{"severity": "P1"}` and the instruction, and terminal 2 prints a prompt per P1 alert. Stop and restart the bridge and the watch resumes, delivering the alerts that fired while it was down.

## Headless runs and untrusted payloads

`HeadlessClaudeSink` runs `claude -p --output-format json --allowedTools <list>` and writes the prompt to standard input, so a payload never shows in the process list. The prompt states the user's instruction, then fences the payload between markers that carry a fresh random nonce, with a line saying the content is data and not instructions.

The fence makes a payload easier to read as data. It does not make it safe, so the tool list is the real boundary:

- `-allowed-tools` (`AllowedTools`) is every run's list. Keep it to tools that only read.
- `-outward-tools` (`OutwardTools`) are tools that send, post or write. A run gets them only when its `Approver` says yes; otherwise they go to `--disallowedTools` and the prompt says they were not approved. The default approver denies. `-approve-outward` approves every run, unattended, and is for rules the user has decided to trust.
- `-mcp-config` passes `--mcp-config <file> --strict-mcp-config`, so a run sees only the MCP servers in that file.

Any other `eventsclient.EventSink` can stand in for the headless one.

## State

`FileStore` keeps watches, secrets and cursors in one JSON file, created `0600` and replaced atomically on every change. On start the bridge resubscribes each active watch with its stored secret and cursor. Upstream that is a refresh of the same subscription, and delivery resumes after the last event the sink was given. A watch the bridge cannot resubscribe stays listed with its error. One the server ended (`type: terminated`) is marked `terminated` and stays until `unwatch`.

## Go API

```go
b, err := bridge.New(bridge.Config{
    Upstreams: []bridge.Upstream{{Name: "ops", Client: connectedClient}},
    PublicURL: "https://bridge.example.com",
    Sink:      &bridge.HeadlessClaudeSink{AllowedTools: []string{"Read"}},
    Store:     bridge.NewFileStore("watches.json"),
})
mux.Handle(bridge.HookPathPrefix, b.Handler())
b.Register(mcpServer)
b.Start(ctx) // after the mux is serving: resubscribing answers endpoint verification
```
