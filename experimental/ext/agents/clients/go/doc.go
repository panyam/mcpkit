// Package agentsclient is the Go-side SDK for the experimental agents
// discovery primitive (agents-wg#22). It wraps a *client.Client with the
// progressive-disclosure host workflow: detect the capability, read the
// roster, then resolve one agent's details.
//
// EXPERIMENTAL: tracks experimental/ext/agents, which follows an unmerged SEP
// and may still change.
//
// Typical host workflow:
//
//	mcp := client.NewClient(serverURL, info)
//	mcp.Connect()
//	ac := agentsclient.New(mcp)
//	if !ac.SupportsAgents() {
//	    return // server has no agents; fall back to plain tools/list
//	}
//	roster, _ := ac.ListAgents(ctx)            // level 2: cards, no schemas
//	chosen := route(roster)                    // host/model picks by description and capabilities
//	details, err := ac.GetAgent(ctx, chosen)   // level 3: instructions and scoped tools
//	if agentsclient.IsAgentNotFound(err) {
//	    // the roster changed; re-list and route again
//	}
//	// The host builds a local subagent from details.Instructions and
//	// details.Tools, and calls those tools through tools/call.
package agentsclient
