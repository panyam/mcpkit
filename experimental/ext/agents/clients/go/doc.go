// Package agentsclient is the Go-side SDK for the experimental agents
// discovery primitive (agents-wg#20). It wraps a *client.Client with the
// progressive-disclosure host workflow: detect the capability, read the
// roster, then resolve one agent's detail.
//
// EXPERIMENTAL: tracks experimental/ext/agents, which is pre-SEP and will
// churn. Decoders are deliberately tolerant so a wire-shape tweak on the
// server does not hard-break a host.
//
// Typical host workflow:
//
//	mcp := client.NewClient(serverURL, info)
//	mcp.Connect()
//	ac := agentsclient.New(mcp)
//	if !ac.SupportsAgents() {
//	    return // server has no agents; fall back to plain tools/list
//	}
//	roster, _ := ac.ListAgents(ctx)          // level 2: summaries, no schemas
//	chosen := route(roster)                  // host/model picks by description+capabilities
//	detail, _ := ac.GetAgent(ctx, chosen)    // level 3: instructions + scoped tools
//	// invoke rides on tools/call:
//	mcp.ToolCall(detail.DelegateTool, map[string]any{"query": task})
package agentsclient
