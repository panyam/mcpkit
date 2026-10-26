package stateless

import (
	"context"

	core "github.com/panyam/mcpkit/core"
)

// RequestMetaFromContext returns the validated SEP-2575 _meta envelope
// attached to ctx for a request that arrived over the stateless wire, or nil
// otherwise. Thin re-export of core.RequestMetaFromContext so packages already
// importing server/stateless don't need a core import for this one accessor.
//
// Handlers that just want to know whether a capability is declared
// should prefer the typed ctx.ClientCaps() accessor on ToolContext or
// PromptContext; this raw view is for the rare path that needs
// protocolVersion or clientInfo directly.
func RequestMetaFromContext(ctx context.Context) *core.RequestMeta {
	return core.RequestMetaFromContext(ctx)
}
