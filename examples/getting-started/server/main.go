// Command server is the minimal mcpkit MCP server used by the Get Started
// guide: one typed tool, Streamable HTTP, no extras. Keep it tiny — the docs
// extract these snippets verbatim, so every line here is teaching surface.
package main

import (
	"log"
	"time"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// greetInput is the tool's typed input. mcpkit derives the JSON Schema from
// these struct tags at registration time — you never hand-write the schema,
// and the handler receives a decoded, validated greetInput.
type greetInput struct {
	Name string `json:"name" jsonschema:"description=Who to greet,required"`
}

func main() {
	srv := server.NewServer(
		core.ServerInfo{Name: "getting-started", Version: "0.1.0"},
		server.WithToolTimeout(30*time.Second),
	)

	// TextTool[In] auto-generates the input schema from greetInput and wraps
	// the returned string as tool text output. No map[string]any schema, no
	// manual Bind().
	srv.Register(core.TextTool[greetInput]("greet", "Say hello to someone by name",
		func(ctx core.ToolContext, input greetInput) (string, error) {
			return "Hello, " + input.Name + "!", nil
		},
	))

	log.Println("getting-started server listening on http://localhost:8787/mcp")
	if err := srv.Run(":8787"); err != nil {
		log.Fatal(err)
	}
}
