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

// Dispatch routes one stateless-wire request. The returned *core.Response
// always carries a JSON-RPC payload; transport-layer error → HTTP status
// mapping happens in errors.go (HTTPStatusForCode) at the transport boundary.
func (d *Dispatcher) Dispatch(ctx context.Context, req *core.Request) *core.Response {
	id := req.ID
	if id == nil {
		id = json.RawMessage("null")
	}

	// Removed methods short-circuit before _meta validation. The spec is
	// clear these methods MUST NOT exist on the stateless wire, and
	// returning -32601 here is unambiguous regardless of envelope state.
	if _, removed := removedLegacyMethods[req.Method]; removed {
		return core.NewErrorResponse(id, core.ErrCodeMethodNotFound,
			"method not found on SEP-2575 stateless wire: "+req.Method)
	}

	// Every other method requires a valid _meta envelope.
	meta, err := core.DecodeRequestMeta(req.Params)
	if err != nil {
		// MetaValidationError carries the specific missing field for diagnostics.
		return core.NewErrorResponse(id, core.ErrCodeInvalidParams, err.Error())
	}

	// Validate the protocol version against what this server speaks.
	// Transport layer separately validates MCP-Protocol-Version HTTP
	// header alignment via -32001 HeaderMismatch; here we surface a
	// version-unknown failure with -32004 + the supported list.
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
		)
	}

	// Thread the validated envelope through ctx so handlers and
	// downstream helpers (per-request cap checks, log-level gating)
	// can read it without the Backend interface needing a getter.
	ctx = withRequestMeta(ctx, meta)

	switch req.Method {
	case "server/discover":
		return d.handleDiscover(id)
	case "tools/list":
		return d.handleToolsList(id, req.Params)
	case "tools/call":
		return d.handleToolsCall(ctx, id, req.Params)
	case "resources/list":
		return d.handleResourcesList(id, req.Params)
	case "resources/read":
		return d.handleResourcesRead(ctx, id, req.Params)
	case "resources/templates/list":
		return d.handleResourcesTemplatesList(id, req.Params)
	case "prompts/list":
		return d.handlePromptsList(id, req.Params)
	case "prompts/get":
		return d.handlePromptsGet(ctx, id, req.Params)
	case "completion/complete":
		return d.handleCompletionComplete(ctx, id, req.Params)
	case "tasks/get", "tasks/update", "tasks/cancel":
		// SEP-2663 tasks extension methods. Routing goes through the
		// backend's middleware-aware path so:
		//   - extension capability gating (handler emits -32003 if the
		//     per-request _meta.io.modelcontextprotocol/clientCapabilities
		//     omits the extension declaration), and
		//   - any server-level middleware (auth, logging, future extensions)
		//     applies on the stateless wire too.
		//
		// Backends without middleware support (test fakes) fall through
		// to -32601 below — which mirrors the legacy "method not found"
		// for the same scenario.
		out := &core.Request{
			JSONRPC: "2.0",
			ID:      id,
			Method:  req.Method,
			Params:  req.Params,
		}
		if resp, ok := d.Backend.InvokeWithMiddleware(ctx, out); ok {
			return resp
		}
		return core.NewErrorResponse(id, core.ErrCodeMethodNotFound,
			"method not found: "+req.Method)
	default:
		return core.NewErrorResponse(id, core.ErrCodeMethodNotFound,
			"method not found: "+req.Method)
	}
}

// requestMetaCtxKey is the unexported ctx key under which the validated
// per-request _meta envelope is threaded to handlers and helpers.
type requestMetaCtxKey struct{}

// withRequestMeta attaches the validated envelope to ctx.
func withRequestMeta(ctx context.Context, meta *core.RequestMeta) context.Context {
	return context.WithValue(ctx, requestMetaCtxKey{}, meta)
}

// RequestMetaFromContext returns the validated SEP-2575 _meta envelope
// attached to ctx by the stateless dispatcher, or nil if the call did
// not arrive over the stateless wire. Handlers that need to gate on
// per-request caps should prefer core.ClientSupportsExtensionForRequest;
// this accessor is for the rare path that needs protocolVersion or
// clientInfo directly.
func RequestMetaFromContext(ctx context.Context) *core.RequestMeta {
	if v, ok := ctx.Value(requestMetaCtxKey{}).(*core.RequestMeta); ok {
		return v
	}
	return nil
}
