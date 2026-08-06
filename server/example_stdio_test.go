package server_test

import (
	"context"
	"log"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server"
)

// Serving over stdio. This is the transport a desktop host uses when it
// launches your server as a subprocess: one JSON-RPC message per line on
// stdin and stdout.
//
// Nothing else may write to stdout, or it corrupts the framing. Send logs to
// stderr, which the host captures separately. RunStdio blocks until ctx is
// cancelled or stdin closes.
//
// This example is illustrative and does not run: RunStdio blocks and consumes
// the process's stdin.
func ExampleServer_RunStdio() {
	srv := server.NewServer(core.ServerInfo{Name: "example", Version: "1.0"})
	srv.Register(core.TextTool[struct{}]("ping", "Replies with pong",
		func(ctx core.ToolContext, _ struct{}) (string, error) {
			return "pong", nil
		},
	))

	// log's default output is stderr, which is the safe channel here.
	log.SetPrefix("example-server: ")

	if err := srv.RunStdio(context.Background()); err != nil {
		log.Fatal(err)
	}
}
