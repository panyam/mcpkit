// Command rooms-channel is the stdio MCP server Claude Code starts as a channel
// for a room bridge: the bridge's tools become the session's, and what happens
// on the pages of an attached room arrives in the session as a channel
// message. See the module README for the Claude Code setup.
//
//	rooms-channel -bridge https://viewer.example.com/mcp
//
// The bridge token is read from ROOMS_BRIDGE_TOKEN rather than a flag, so it
// stays out of the process list. Logs go to stderr; stdout carries MCP.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/panyam/mcpkit/experimental/integrations/roomsevents/channel"
)

func main() {
	bridge := flag.String("bridge", "", "the room bridge's MCP endpoint (required)")
	name := flag.String("name", "rooms", "server name, and the source attribute on each <channel> tag")
	flag.Parse()
	if *bridge == "" {
		fmt.Fprintln(os.Stderr, "rooms-channel: -bridge is required")
		os.Exit(2)
	}

	logger := log.New(os.Stderr, "", log.LstdFlags)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := channel.Run(ctx, channel.Config{
		BridgeURL: *bridge,
		Token:     os.Getenv("ROOMS_BRIDGE_TOKEN"),
		Name:      *name,
		Logf:      logger.Printf,
	}, os.Stdin, os.Stdout)
	if err != nil && ctx.Err() == nil {
		logger.Printf("rooms-channel: %v", err)
		os.Exit(1)
	}
}
