package server

import (
	"context"
	"encoding/json"

	core "github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/server/stateless"
)

// removedStatelessMethods are the methods SEP-2575 removed from the stateless
// wire. Any of them arriving there is -32601, which the transport maps to
// HTTP 404, before the _meta envelope is even looked at.
var removedStatelessMethods = map[string]struct{}{
	"initialize":                       {},
	"notifications/initialized":        {},
	"initialized":                      {},
	"ping":                             {},
	"logging/setLevel":                 {},
	"resources/subscribe":              {},
	"resources/unsubscribe":            {},
	"notifications/cancelled":          {},
	"notifications/roots/list_changed": {},
}

// dispatchStateless serves one SEP-2575 stateless-wire request, for the HTTP
// and stdio transports alike. It owns what only this wire has: the
// removed-methods -32601, the _meta envelope and version checks, and stamping
// serverInfo and resultType onto every success result (spec PR 3002; there is
// no initialize to carry the server's identity). Everything else is
// runRequest with wireStateless, the same request path and method table the
// session wire uses (#1625), so no method can skip the middleware chain.
//
// A non-nil error is a middleware short-circuit (typically *core.AuthError),
// returned raw with a nil response so the transport's writeAuthError sends
// the same 403 + WWW-Authenticate as the session wire (issue 815). Error
// responses are not stamped: the schema puts ResultMetaObject on Result only.
func (s *Server) dispatchStateless(ctx context.Context, req *core.Request) (*core.Response, error) {
	resp, err := s.dispatchStatelessUnstamped(ctx, req)
	if err == nil && resp != nil && resp.Error == nil {
		resp.Result = core.InjectServerInfoIntoResult(resp.Result, s.dispatcher.serverInfo)
		// The stateless wire speaks only 2026-07-28, so every success result
		// carries resultType, with the MRTR and tasks variants keeping their
		// caller-set values.
		resp.Result = core.InjectResultTypeIntoResult(resp.Result)
	}
	return resp, err
}

func (s *Server) dispatchStatelessUnstamped(ctx context.Context, req *core.Request) (*core.Response, error) {
	id := req.ID
	if id == nil {
		id = json.RawMessage("null")
	}

	// Removed methods fail before the envelope check: they MUST NOT exist on
	// this wire whatever the envelope says.
	if _, removed := removedStatelessMethods[req.Method]; removed {
		return core.NewErrorResponse(id, core.ErrCodeMethodNotFound,
			"method not found on SEP-2575 stateless wire: "+req.Method), nil
	}

	meta, err := core.DecodeRequestMetaFromRawJSON(&req.Params)
	if err != nil {
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error()), nil
	}

	// The transport has already cross-checked the MCP-Protocol-Version header
	// against _meta (-32020); this is the "do we speak it at all" check.
	supported := core.SupportedStatelessVersions
	versionOK := false
	for _, sv := range supported {
		if sv == meta.ProtocolVersion {
			versionOK = true
			break
		}
	}
	if !versionOK {
		return core.NewErrorResponseWithData(id, core.ErrCodeUnsupportedProtocolVersion,
			"unsupported protocol version: "+meta.ProtocolVersion,
			core.UnsupportedProtocolVersionData{Supported: supported, Requested: meta.ProtocolVersion},
		), nil
	}

	// Handlers and middleware read per-request caps from this envelope, since
	// there is no initialize to have stored them.
	ctx = core.WithRequestMeta(ctx, meta)

	d := s.dispatcher
	return s.runRequest(ctx, wireStateless, d, &core.Request{JSONRPC: "2.0", ID: id, Method: req.Method, Params: req.Params}, sessionIO{},
		func(ctx context.Context, req *core.Request) (resp *core.Response, err error) {
			defer recoverHandlerPanic(req, &resp)
			return d.route(ctx, wireStateless, req.ID, req), nil
		})
}

// discoverResult is the server/discover result: what this server speaks and
// offers right now. The method table answers server/discover with it on the
// stateless wire.
func (d *Dispatcher) discoverResult() stateless.DiscoverResult {
	return stateless.DiscoverResult{
		SupportedVersions: core.SupportedStatelessVersions,
		Capabilities:      d.statelessCapabilities(),
		Instructions:      d.instructions,
		TTLMs:             0,
		CacheScope:        core.CacheScopePublic,
	}
}

// statelessCapabilities is the capability shape server/discover and
// subscriptions/listen advertise, derived from registered handlers and active
// extensions. It mirrors the initialize handshake's computation, minus the
// session-only fields.
func (d *Dispatcher) statelessCapabilities() core.ServerCapabilities {
	caps := core.ServerCapabilities{}

	r := d.Reg
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

	if exts := d.extensions; len(exts) > 0 {
		caps.Extensions = make(map[string]core.ExtensionCapability, len(exts))
		for id, e := range exts {
			caps.Extensions[id] = core.ExtensionCapability(e.Settings)
		}
		caps.TopLevel = extensionTopLevel(exts)
	}
	return caps
}
