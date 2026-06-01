package client

import (
	"net/http"

	"github.com/panyam/mcpkit/core"
)

// SEP-2243 client-side wiring.
//
// The pure wire-format rules (header names, value encoding, schema
// validation, Mcp-Name derivation) live in core/sep2243.go. This file
// holds the client-only glue that mutates outbound *http.Request — the
// only piece that isn't transferable to the server side.

// setSEP2243RoutingHeaders attaches the SEP-2243 standard routing headers
// to a streamable HTTP request. method is the JSON-RPC method carried in
// the body and is always mirrored onto Mcp-Method; cc may carry a per-call
// mcpName (derived centrally in rawCallWithContext for tools/call /
// prompts/get / resources/read) that is mirrored onto Mcp-Name. Passing
// nil cc, or a cc without mcpName set, simply skips Mcp-Name — Mcp-Method
// is unconditional.
func setSEP2243RoutingHeaders(req *http.Request, method string, cc *CallContext) {
	if method != "" {
		req.Header.Set(core.McpMethodHeader, method)
	}
	if cc != nil && cc.mcpName != "" {
		req.Header.Set(core.McpNameHeader, cc.mcpName)
	}
}

// Thin package-local wrappers so existing call sites in client/client.go
// stay byte-identical. New code may call the core helpers directly; these
// stay until the wrappers' call sites get cleaned up in a follow-up.

func extractMcpParamHeaders(inputSchema any) map[string]string {
	return core.ExtractMcpHeaderParams(inputSchema)
}

func validateMcpParamHeaders(inputSchema any) error {
	return core.ValidateMcpHeaderSchema(inputSchema)
}

func mcpParamHeaderName(fragment string) string {
	return core.McpParamHeaderName(fragment)
}

func encodeMcpParamHeaderValue(v any) (string, bool) {
	return core.EncodeMcpHeaderValue(v)
}

func deriveMcpName(method string, params any) string {
	return core.DeriveMcpName(method, params)
}
