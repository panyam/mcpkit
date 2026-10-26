package server

import (
	"context"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

// statelessBackend adapts a *Server (with its embedded Dispatcher and
// Registry) to the stateless.Backend interface. Lives in package server
// rather than server/stateless so it can read registry private fields
// without exporting more API surface.
//
// Constructed by newStreamableTransport (unless the mode is
// stateless.ModeLegacyOnly) and by the stdio transport.
type statelessBackend struct {
	discoverSource
	s *Server
}

// newStatelessBackend builds a Backend bound to the given server.
func newStatelessBackend(s *Server) *statelessBackend {
	return &statelessBackend{discoverSource: discoverSource{s.dispatcher}, s: s}
}

// Compile-time check: the adapter satisfies the stateless package's
// interface. If the interface ever gains a method, this line breaks
// first — loudest signal we can give.
var _ stateless.Backend = (*statelessBackend)(nil)

// discoverSource answers server/discover's questions from a Dispatcher. The
// shared method table builds the discover result from it, and
// statelessBackend embeds it to satisfy stateless.Backend.
type discoverSource struct {
	d *Dispatcher
}

// ServerInfo returns the server identity stamped into every stateless
// result's _meta. Sourced from NewServer's first argument.
func (ds discoverSource) ServerInfo() core.Implementation {
	return ds.d.serverInfo
}

// Instructions returns the WithInstructions text for server/discover.
func (ds discoverSource) Instructions() string {
	return ds.d.instructions
}

// Capabilities returns the capability shape derived from registered
// handlers + active extensions. Mirrors the legacy initialize handshake's
// computation modulo session-only fields.
func (ds discoverSource) Capabilities() core.ServerCapabilities {
	caps := core.ServerCapabilities{}

	r := ds.d.Reg
	r.mu.RLock()
	hasTools := len(r.tools) > 0
	hasResources := len(r.resources) > 0 || len(r.templates) > 0
	hasPrompts := len(r.prompts) > 0
	hasCompletions := len(r.completions) > 0
	r.mu.RUnlock()

	if hasTools {
		caps.Tools = &core.ToolsCap{ListChanged: true}
	}
	if hasResources {
		caps.Resources = &core.ResourcesCap{ListChanged: true}
	}
	if hasPrompts {
		caps.Prompts = &core.PromptsCap{ListChanged: true}
	}
	if hasCompletions {
		caps.Completions = &struct{}{}
	}

	if exts := ds.d.extensions; len(exts) > 0 {
		caps.Extensions = make(map[string]core.ExtensionCapability, len(exts))
		for id, e := range exts {
			caps.Extensions[id] = core.ExtensionCapability(e.Settings)
		}
		caps.TopLevel = extensionTopLevel(exts)
	}
	return caps
}

// SupportedVersions returns the stateless-wire version list this build
// speaks. Distinct from the legacy initialize negotiation list — the
// two surfaces are advertised independently.
func (ds discoverSource) SupportedVersions() []string {
	return core.SupportedStatelessVersions
}

// InvokeWithMiddleware runs req through Server.runRequest with wireStateless,
// so the stateless wire gets the same per-request context, user middleware,
// metrics and trace middleware as the session wire (#1625). The stateless
// dispatcher calls it for every method past its envelope checks, and the
// terminal is the Dispatcher's route, the method table both wires share,
// called with wireStateless. server/discover is answered there, inside the
// chain, so a middleware sees it the way it sees initialize on the session
// wire. The terminal recovers a handler panic into -32603, as the session
// wire's Dispatch does.
//
// Background goroutines spawned from middleware (e.g., the v2 task runner)
// have no session-level notify path on the stateless wire — there's no
// persistent GET SSE stream to push notifications onto. notifications/tasks
// emissions silently drop, which matches the SEP-2575 design (no server-
// initiated push); clients observe state via tasks/get polling.
//
// By default, stateless task store entries key under sessionID="" (no
// session), so stateless tasks share one bucket per process. Multi-tenant
// deployments install a server.WithTaskBucketKeyer that derives the bucket
// from an authenticated subject; runRequest puts it on ctx for both wires
// (issue 485).
//
// A non-nil error is a middleware short-circuit (typically *core.AuthError),
// returned raw so the transport's writeAuthError emits the same HTTP status +
// WWW-Authenticate as the session wire (issue 815).
func (b *statelessBackend) InvokeWithMiddleware(ctx context.Context, req *core.Request) (*core.Response, error) {
	d := b.s.dispatcher
	return b.s.runRequest(ctx, wireStateless, d, req, sessionIO{},
		func(ctx context.Context, req *core.Request) (resp *core.Response, err error) {
			defer recoverHandlerPanic(req, &resp)
			return d.route(ctx, wireStateless, req.ID, req), nil
		})
}
