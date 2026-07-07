# Get Started

mcpkit is a batteries-included Go SDK for the [Model Context
Protocol](https://modelcontextprotocol.io). You get a spec-conformant server and
client, plus working implementations of a stack of draft and recent SEPs — auth,
long-running tasks, interactive apps, tracing, events, skills — that most SDKs
haven't shipped yet.

This page takes you from nothing to a running server-and-client in about two
minutes. Every snippet below is lifted from
[`examples/getting-started`](https://github.com/panyam/mcpkit/tree/main/examples/getting-started),
which is compiled in CI — so it works.

## Install

```bash
go get github.com/panyam/mcpkit
```

Requires Go 1.26+.

## Your first server

A server exposes **tools** the model can call. Define the input as a Go struct;
mcpkit derives the JSON Schema from its tags and hands your handler a decoded,
validated value. No hand-written schema, no manual argument parsing.

```go
// server/main.go
package main

import (
	"log"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// greetInput is the tool's typed input. mcpkit turns these struct tags into
// the tool's JSON Schema at registration time.
type greetInput struct {
	Name string `json:"name" jsonschema:"description=Who to greet,required"`
}

func main() {
	srv := server.NewServer(
		core.ServerInfo{Name: "getting-started", Version: "0.1.0"},
		server.WithToolTimeout(30*time.Second),
	)

	// TextTool[In] auto-generates the schema from greetInput and wraps the
	// returned string as tool output.
	srv.Register(core.TextTool[greetInput]("greet", "Say hello to someone by name",
		func(ctx core.ToolContext, input greetInput) (string, error) {
			return "Hello, " + input.Name + "!", nil
		},
	))

	log.Println("listening on http://localhost:8787/mcp")
	if err := srv.Run(":8787"); err != nil { // Streamable HTTP
		log.Fatal(err)
	}
}
```

That's a complete, spec-conformant MCP server over Streamable HTTP.

## Your first client

The client connects to a server and calls its tools. `ToolCall` sends
`tools/call` and returns the text result directly.

```go
// client/main.go
package main

import (
	"fmt"
	"log"

	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

func main() {
	c := client.NewClient("http://localhost:8787/mcp",
		core.ClientInfo{Name: "getting-started-client", Version: "0.1.0"},
	)
	if err := c.Connect(); err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer c.Close()

	out, err := c.ToolCall("greet", map[string]any{"name": "world"})
	if err != nil {
		log.Fatalf("greet: %v", err)
	}
	fmt.Println(out) // Hello, world!
}
```

## Run it

Two terminals:

```bash
# terminal 1 — the server
go run ./server

# terminal 2 — the client
go run ./client
# → Hello, world!
```

## Connect a real MCP host

The same server works with any MCP host. Point Claude Code at it:

```bash
claude mcp add my-server --transport streamable-http http://localhost:8787/mcp
```

Or add it to Claude Desktop / VS Code MCP settings:

```json
{
  "mcpServers": {
    "my-server": { "type": "streamable-http", "url": "http://localhost:8787/mcp" }
  }
}
```

## Next steps

You have a working server and client. Now reach for the batteries:

- **[Guides](../guides/transports/)** — task-oriented how-tos: transports, auth (JWT / OAuth /
  DCR), long-running tasks, interactive apps, tracing, deployment.
- **[Examples](../examples/)** — 20+ runnable, guided walkthroughs. This is where the
  draft-SEP batteries are shown end to end.
- **[API reference](https://pkg.go.dev/github.com/panyam/mcpkit)** — the full Go API on
  pkg.go.dev.
- **[Conformance](../conformance/)** — exactly which spec and SEP scenarios mcpkit
  passes, refreshed by CI.
