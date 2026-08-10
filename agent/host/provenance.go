package host

import (
	"context"
	"time"

	"github.com/panyam/mcpkit/agent"
)

// Built-in source ids whose output the host vouches for. These are the only
// sources that derive ProvenanceOperator, and the list is closed on purpose.
//
// Operator means the process computed the output rather than relaying it, and
// the host can only honestly claim that for its own in-repo, non-relaying
// tools. An extension is arbitrary code that may shell out or fetch, so it is
// never vouched for by derivation — config has to say so explicitly. Getting
// this backwards is the one mistake that silently disables the mitigation
// rather than making it noisy.
const (
	sourceHost          = "host"
	sourceRunnerControl = "runner-control"
)

// Prefixes the host uses when adding sources that are themselves agents. A
// sub-agent's output is a relay of whatever it read, so it is marked, but it
// is worth distinguishing from open-web content when a Mark renders it.
var agentSourcePrefixes = []string{"subagent:", "fanout:", "serveragents:"}

// ownerLookupTimeout bounds the source lookup a classification does. Short
// because the index is memoized and the common path does no I/O at all; the
// timeout only bites when a re-list has to reach a server that is not
// answering, and marking is the right answer in that case anyway.
const ownerLookupTimeout = 2 * time.Second

// recordOrigin notes what a source id is, for provenance derivation. Called
// where the source is added, since a server id and an extension name are
// indistinguishable strings afterwards.
func (a *App) recordOrigin(id string, p agent.Provenance) {
	if a.toolOrigins == nil {
		a.toolOrigins = map[string]agent.Provenance{}
	}
	a.toolOrigins[id] = p
}

// derivedProvenance maps a source id to a label, or "" when the host has no
// honest claim to make.
//
// The recorded map wins, then the built-in allowlist, then the agent prefixes.
// Anything else — an extension, a source added by a path that forgot to
// record — falls through to "" and the caller defaults it to world. Unknown
// resolving to marked is the safe direction; the alternative is a source
// nobody classified reaching the model unfenced.
func (a *App) derivedProvenance(id string) agent.Provenance {
	if p, ok := a.toolOrigins[id]; ok {
		return p
	}
	switch id {
	case sourceHost, sourceRunnerControl:
		return agent.ProvenanceOperator
	}
	for _, prefix := range agentSourcePrefixes {
		if len(id) >= len(prefix) && id[:len(prefix)] == prefix {
			return agent.ProvenanceAgent
		}
	}
	return ""
}

// classifyTool is the spotlight classifier: explicit config first, then
// derivation from the source the call resolves to, then world.
//
// It resolves through MultiSource.OwnerOf rather than matching the tool name
// itself, because the name the model called may be the qualified
// "sourceID_name" form that collision handling produces. Re-deriving that
// mapping here would be a second copy of resolution logic that drifts from the
// one Call uses.
//
// Evaluated per call rather than snapshotted at construction, so a server
// connected later, or an extension registered after the middleware was built,
// is classified rather than silently treated as unknown.
//
// The lookup is bounded by ownerLookupTimeout because agent.SpotlightConfig's
// Classify takes no context, and a miss against the memoized index makes
// MultiSource re-list every source — which reaches the network. An unreachable
// server must not be able to stall tool dispatch; a timeout resolves to world,
// which marks.
func (a *App) classifyTool(labels map[string]agent.Provenance) func(agent.ToolCallInfo) agent.Provenance {
	return func(info agent.ToolCallInfo) agent.Provenance {
		if p, ok := labels[info.Call.Name]; ok {
			return p
		}
		if a.sources == nil {
			return agent.ProvenanceWorld
		}
		args := map[string]any{}
		if info.Call.Args.Len() > 0 {
			_ = info.Call.Args.Bind(&args)
		}
		ctx, cancel := context.WithTimeout(context.Background(), ownerLookupTimeout)
		defer cancel()
		id, found := a.sources.OwnerOf(ctx, info.Call.Name, args)
		if !found {
			return agent.ProvenanceWorld
		}
		if p := a.derivedProvenance(id); p != "" {
			return p
		}
		return agent.ProvenanceWorld
	}
}
