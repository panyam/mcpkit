package stateless

import (
	"context"
	"encoding/json"

	core "github.com/panyam/mcpkit/core"
)

// Dispatcher routes SEP-2575 stateless-wire requests.
//
// Process-shared (one per Server) — no per-request state lives on it.
// Every call carries its own _meta envelope which the dispatcher
// validates up front and threads through context so handlers can read
// per-request capabilities via core.ClientSupportsExtensionForRequest.
//
// Method coverage on this commit:
//
//	server/discover                tools/list   tools/call
//	resources/list                 resources/read
//	resources/templates/list       prompts/list   prompts/get
//	completion/complete
//
// Removed legacy methods (initialize, ping, logging/setLevel,
// resources/(un)subscribe, etc.) short-circuit to -32601; the transport
// layer maps that to HTTP 404. subscriptions/listen and the MRTR-routed
// sample/elicit path land in follow-up commits.
type Dispatcher struct {
	Backend Backend
}

// New constructs a Dispatcher bound to the given backend. The Server
// constructs one of these in transport-setup time and routes requests
// through it whenever the per-request shape signals the stateless wire.
func New(b Backend) *Dispatcher {
	return &Dispatcher{Backend: b}
}

// removedLegacyMethods enumerates the methods the SEP-2575 stateless wire
// explicitly removed. Any of these arriving on the stateless path MUST
// return -32601 (method not found) and the transport MUST map to HTTP 404.
var removedLegacyMethods = map[string]struct{}{
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

// Dispatch routes one stateless-wire request, returning (*core.Response, error).
//
// On the happy path err is nil and the *core.Response carries a JSON-RPC
// payload; transport-layer error → HTTP status mapping happens in errors.go
// (HTTPStatusForCode) at the transport boundary.
//
// A non-nil error is a middleware short-circuit (typically *core.AuthError)
// raised by the backend's middleware chain on tools/call, prompts/get, or a
// custom method. The transport surfaces it via writeAuthError — emitting the
// correct HTTP status (e.g. 403) + WWW-Authenticate header — so the stateless
// wire matches the legacy wire's auth signaling instead of folding it into a
// generic -32603 body (issue 815). When err is non-nil the *core.Response is nil.
func (d *Dispatcher) Dispatch(ctx context.Context, req *core.Request) (*core.Response, error) {
	id := req.ID
	if id == nil {
		id = json.RawMessage("null")
	}

	// Removed methods short-circuit before _meta validation. The spec is
	// clear these methods MUST NOT exist on the stateless wire, and
	// returning -32601 here is unambiguous regardless of envelope state.
	if _, removed := removedLegacyMethods[req.Method]; removed {
		return core.NewErrorResponse(id, core.ErrCodeMethodNotFound,
			"method not found on SEP-2575 stateless wire: "+req.Method), nil
	}

	// Every other method requires a valid _meta envelope.
	meta, err := core.DecodeRequestMetaFromRawJSON(req.ParamsLazy())
	if err != nil {
		// MetaValidationError carries the specific missing field for diagnostics.
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error()), nil
	}

	// Validate the protocol version against what this server speaks.
	// Transport layer separately validates MCP-Protocol-Version HTTP
	// header alignment via -32020 HeaderMismatch; here we surface a
	// version-unknown failure with -32022 + the supported list.
	supported := d.Backend.SupportedVersions()
	versionOK := false
	for _, sv := range supported {
		if sv == meta.ProtocolVersion {
			versionOK = true
			break
		}
	}
	if !versionOK {
		return core.NewErrorResponseWithData(
			id,
			core.ErrCodeUnsupportedProtocolVersion,
			"unsupported protocol version: "+meta.ProtocolVersion,
			core.UnsupportedProtocolVersionData{
				Supported: supported,
				Requested: meta.ProtocolVersion,
			},
		), nil
	}

	// Thread the validated envelope through ctx so handlers and
	// downstream helpers (per-request cap checks, log-level gating)
	// can read it without the Backend interface needing a getter.
	// The ctx key lives in core so handler accessors like
	// ctx.ClientCaps() can read it without an import cycle.
	ctx = core.WithRequestMeta(ctx, meta)

	// Handlers that route through the backend's middleware chain
	// (tools/call, prompts/get, and the default custom-method branch)
	// return (resp, err); the err is a middleware short-circuit forwarded
	// to the transport's writeAuthError. The remaining handlers can't raise
	// a middleware auth error, so they're wrapped with a nil error here.
	switch req.Method {
	case "server/discover":
		return d.handleDiscover(id), nil
	case "tools/list":
		return d.handleToolsList(id, req.Params), nil
	case "tools/call":
		return d.handleToolsCall(ctx, id, req.Params)
	case "resources/list":
		return d.handleResourcesList(id, req.Params), nil
	case "resources/read":
		return d.handleResourcesRead(ctx, id, req.Params), nil
	case "resources/templates/list":
		return d.handleResourcesTemplatesList(id, req.Params), nil
	case "prompts/list":
		return d.handlePromptsList(id, req.Params), nil
	case "prompts/get":
		return d.handlePromptsGet(ctx, id, req.Params)
	case "completion/complete":
		return d.handleCompletionComplete(ctx, id, req.Params), nil
	default:
		// Any other method (custom JSON-RPC verbs registered via
		// Server.HandleMethod — events/poll, events/list,
		// events/subscribe, tasks/get|update|cancel, future SEPs,
		// caller-defined endpoints) goes through the backend's
		// middleware-aware path so:
		//   - extension capability gating (handler emits -32021 if the
		//     per-request _meta.io.modelcontextprotocol/clientCapabilities
		//     omits the extension declaration), and
		//   - any server-level middleware (auth, logging, tracing)
		//     applies on the stateless wire too.
		//
		// Backends without middleware support (test fakes) return
		// ok=false here — which surfaces as -32601 "method not found"
		// below, mirroring the legacy dispatcher's behavior for the
		// same scenario.
		out := &core.Request{
			JSONRPC: "2.0",
			ID:      id,
			Method:  req.Method,
			Params:  req.Params,
		}
		if resp, err, ok := d.Backend.InvokeWithMiddleware(ctx, out); ok {
			return resp, err
		}
		return core.NewErrorResponse(id, core.ErrCodeMethodNotFound,
			"method not found: "+req.Method), nil
	}
}

// RequestMetaFromContext returns the validated SEP-2575 _meta envelope
// attached to ctx by the stateless dispatcher, or nil if the call did
// not arrive over the stateless wire. Thin re-export of
// core.RequestMetaFromContext so packages already importing
// server/stateless don't need to add a core import for this one accessor.
//
// Handlers that just want to know whether a capability is declared
// should prefer the typed ctx.ClientCaps() accessor on ToolContext or
// PromptContext; this raw view is for the rare path that needs
// protocolVersion or clientInfo directly.
func RequestMetaFromContext(ctx context.Context) *core.RequestMeta {
	return core.RequestMetaFromContext(ctx)
}
