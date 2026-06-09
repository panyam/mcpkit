package main

// SEP-2575 request-metadata driver for the upstream `request-metadata`
// scenario.
//
// The scenario's mock server is a bare HTTP server — no MCP initialize
// handshake — but it DOES handle `server/discover` (returning a
// 2026-07-28 supported version), which means a stateless-wire mcpkit
// client connects cleanly. After connect, every subsequent POST carries
// the SEP-2575 `_meta` envelope (protocolVersion, clientInfo,
// clientCapabilities) and the `MCP-Protocol-Version` header — exactly
// what the scenario grades:
//
//   - sep-2575-http-client-sends-version-header
//   - sep-2575-client-populates-meta
//   - sep-2575-http-version-header-matches-meta
//   - sep-2575-client-declares-{roots,sampling,elicitation}-capability
//   - sep-2575-client-retry-supported-version (WARNING — client doesn't
//     yet retry on -32001; tracked separately)
//
// We use `ClientModeStateless` rather than `Adaptive` so the test
// fails fast if the server somehow declined stateless — the scenario
// has no meaning on the legacy wire, and falling back silently would
// mask the audit row.

import (
	"github.com/panyam/mcpkit/client"
	"github.com/panyam/mcpkit/core"
)

func driveRequestMetadata(serverURL string) error {
	c := client.NewClient(serverURL,
		core.ClientInfo{Name: "mcpkit-testclient", Version: "0.1.0"},
		client.WithClientMode(client.ClientModeStateless),
		client.WithElicitationHandler(conformanceElicitationHandler),
	)
	if err := c.Connect(); err != nil {
		return err
	}
	defer c.Close()

	// One follow-up POST after Connect is enough — the scenario's checks
	// are per-request and all emit on the first non-discover call. We
	// pick tools/list because the mock server returns a generic
	// {tools: []} response for it; tools/call would also work.
	if _, err := c.ListTools(); err != nil {
		// Non-fatal — the scenario's first response is a synthetic 400
		// with `Unsupported protocol version` to grade the retry path,
		// which mcpkit's client doesn't yet implement (tracked as a
		// follow-up). The first request still fires the version-header
		// + _meta checks, which is what we care about here.
		_ = err
	}
	return nil
}
