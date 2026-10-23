# events bridge

> **EXPERIMENTAL**, like the rest of `experimental/ext/events`.

Lets a user say "when X happens, do Y" in an MCP host and have it happen later with no chat session open. The bridge is an MCP server facing the host and an [MCP Events](../../../README.md) client of one or more upstream events servers. It subscribes by webhook, with itself as the receiver, and hands each event to a sink. The shipped sink starts a headless `claude -p` run.

```
host (Claude Code) --MCP--> bridge --events/subscribe--> events server
                                  <--webhook POST------
                            bridge --claude -p--> headless run
```

## Tools

Four tools cover every event type on every configured server, and three more answer runs waiting on approval.

| Tool | Does |
|------|------|
| `watch_list(server?, query?)` | Runs `events/list` and returns qualified names (`<server>/<event>`), each with its description and `inputSchema`. `query` keeps only events whose name or description contains every word, so a large catalog stays out of context. Sorted by qualified name. When the sink has a tool list it also reports `tools` (`allowed` and `outward`), the names a watch's `tools` may use. |
| `watch(event, arguments, instruction, tools?, session?)` | Checks `arguments` against the event's `inputSchema`, subscribes, and stores `instruction`. A mistake comes back as a tool error that says what to fix. `tools` limits the rule's runs to part of the daemon's tool list, and `session` (`{id, dir}`) makes each occurrence continue the conversation that set up the watch. Both are covered below. The result lists the tools the rule can use. |
| `unwatch(id)` | Stops the watch and unsubscribes it upstream. |
| `watches()` | Every standing watch with its status and last error, sorted by id. |
| `approvals()` | Runs waiting for approval to use outward-facing tools, oldest first: the event, the rule's instruction, the tools asked for, the head of the payload (`payloadUntrusted`) and when it expires. Present only when the bridge has a `PendingApprover`. |
| `approve(id)`, `deny(id)` | Answer one waiting run. An id that already expired or was answered comes back as an error. |

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
- `-outward-tools` (`OutwardTools`) are tools that send, post or write. A run gets them only when its `Approver` says yes; otherwise they go to `--disallowedTools` and the prompt says they were not approved. What happens next depends on the approver (see below).
- `-mcp-config` passes `--mcp-config <file> --strict-mcp-config`, so a run sees only the MCP servers in that file.

Any other `eventsclient.EventSink` can stand in for the headless one.

### Approving outward-facing tools

By default the daemon asks a person. A run that needs outward tools waits, `approvals` lists it, and `approve(id)` or `deny(id)` answers it from whichever host the user has open. Nobody answering within `-approval-timeout` (15 minutes by default) is a denial, and so is the run's context ending. The other two choices:

- `-approval-timeout 0` denies outward tools at once, which is what a daemon nobody watches should run with.
- `-approve-outward` approves them for every run, unattended, for rules the user has decided to trust. It wins over the timeout.

A waiting run holds back later events for the same watch; other watches carry on. Pending approvals live in memory only. A daemon that restarts has forgotten them, and the runs that were waiting ended with the process. Their events come back from the watch's cursor, so each one asks again.

In Go, set the same `*bridge.PendingApprover` as the sink's `Approver` and as `Config.Approvals`. Its `OnPending` hook is told about each new request, on its own goroutine, which is where a notifier goes: the daemon logs it, and announcing it in the session that made the watch is #1581's live-session sink.

### Limiting a rule to the tools it needs

The daemon's `-allowed-tools` and `-outward-tools` are the most any run can get. A watch can name a subset with `tools`, e.g. `["Read", "mcp__slack__post"]` for a rule that only posts to a channel. Its runs get those, the daemon's other tools go to `--disallowedTools`, and the `Approver` is asked only about the outward tools the rule named, so a rule that names none never asks. A name the daemon doesn't offer is refused at `watch` time with the list it does offer, so a watch can never add a tool. `watch` and `watches` report the effective list either way (`tools`, and `outwardTools` for the ones still needing approval), which is what the user should see before agreeing to a standing rule.

The bridge learns the list from the sink: a sink that implements `ToolScoped` (`HeadlessClaudeSink` does) reports its `ToolCeiling()`. A watch naming tools on a sink that doesn't is refused.

### Continuing the session that asked

By default a run starts with nothing but the instruction and the payload. A watch that carries a `session` instead resumes a fork of that Claude Code session, so the run has the conversation so far, which is handy when the instruction was "deal with it the way we discussed":

```
claude -p --resume <session id> --fork-session --output-format json --allowedTools ... 
```

run in the session's directory, since that is where Claude Code keeps the transcript. `--fork-session` gives the run its own copy, so the original conversation is never written to and can stay open while the run happens. The tool limits and the payload fence are the same as for a fresh run.

Inside Claude Code the model can read its own id from `$CLAUDE_CODE_SESSION_ID`, and the `watch` tool's description tells it to pass that with the working directory. The id must be a UUID and the directory absolute, because the id lands in argv.

When the session is gone (Claude Code answers `No conversation found with session ID`, or the directory no longer exists) the run falls back to a fresh one and `OnResumeFailed` is told; the daemon logs it. Any other failure of a resumed run is returned as it is rather than repeated without the context.

A watch that fires weeks later resumes weeks-old context, which may be more misleading than helpful. Only tie a watch to a session when the instruction leans on it.

## State

`FileStore` keeps watches, secrets and cursors in one JSON file, created `0600` and replaced atomically on every change. On start the bridge resubscribes each active watch with its stored secret and cursor. Upstream that is a refresh of the same subscription, and delivery resumes after the last event the sink was given. A watch the bridge cannot resubscribe stays listed with its error. One the server ended (`type: terminated`) is marked `terminated` and stays until `unwatch`.

## Go API

```go
b, err := bridge.New(bridge.Config{
    Upstreams: []bridge.Upstream{{Name: "ops", Client: connectedClient}},
    PublicURL: "https://bridge.example.com",
    Sink:      &bridge.HeadlessClaudeSink{AllowedTools: []string{"Read"}, OutwardTools: []string{"mcp__gmail__send"}, Approver: approvals},
    Store:     bridge.NewFileStore("watches.json"),
    Approvals: approvals, // approvals := &bridge.PendingApprover{}; omit both for deny-all
})
mux.Handle(bridge.HookPathPrefix, b.Handler())
b.Register(mcpServer)
b.Start(ctx) // after the mux is serving: resubscribing answers endpoint verification
```
