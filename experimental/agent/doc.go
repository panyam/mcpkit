// Package agent is mcpkit's host layer: the agentic loop that turns a set of
// connected MCP servers and an LLM into a working agent.
//
// The module provides four seams (see docs/AGENT_DESIGN.md for the design and
// the tracking epic for delivery order):
//
//   - Provider: streaming LLM access with tool-call support
//   - Runner: the multi-step tool loop, emitting wire-serializable events
//   - ToolSource: tool aggregation across MCP servers and host-local functions
//   - Policy hooks: context injection and event-initiated turns
//
// It deliberately excludes sessions, chat transports, and persistence; those
// belong to the applications (CLIs, web hosts, native shells) that embed it.
//
// agent/ is a separate Go module so that LLM-provider dependencies never
// reach mcpkit's core, server, or client packages.
package agent
