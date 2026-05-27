package client

import (
	"encoding/json"
	"fmt"

	core "github.com/panyam/mcpkit/core"
)

// SEP-2575 client-side wire integration.
//
// Three pieces:
//
//  1. adaptiveProbe — Connect's first call under ClientModeAdaptive.
//     Sends server/discover with a minimal _meta envelope. Success →
//     classify the server as stateless, store discovered metadata,
//     mark c.useStatelessWire = true. -32601 / 404 → fall back to the
//     legacy initialize handshake. Network/transport failure → propagate.
//
//  2. wrapParamsForStatelessWire — every outgoing request, when the
//     client is on the stateless wire, gets its params decorated with
//     the SEP-2575 _meta envelope (protocolVersion, clientInfo,
//     clientCapabilities). The transport additionally stamps the
//     MCP-Protocol-Version HTTP header (see streamable_client_meta.go
//     wiring on the POST path).
//
//  3. tryDecodeJSONRPC — the SEP-2575 wire returns 4xx with a JSON-RPC
//     error body for several conditions (HeaderMismatch -32001,
//     MissingRequiredCap -32003, UnsupportedVersion -32004, Method-
//     NotFound -32601 on removed methods). The transport tries to
//     decode 4xx bodies as JSON-RPC before falling back to the
//     legacy HTTPStatusError shape.

// adaptiveProbe sends server/discover and classifies the response.
// Returns the discover result on success; ok=false when the server
// returned -32601 (no stateless wire); error on any other failure.
//
// Called by Connect under ClientModeAdaptive *before* the legacy
// initialize handshake. The probe carries the same _meta envelope
// the stateless dispatcher requires; servers that ignore _meta but
// understand the method (none currently exist) still get classified
// as stateless via the success path.
func (c *Client) adaptiveProbe() (result *discoverResult, fallback bool, err error) {
	params := map[string]any{
		"_meta": c.buildRequestMeta(),
	}
	resp, callErr := c.rawCall("server/discover", params)
	if callErr != nil {
		// Transport-level failure (network, TLS) — propagate up. The
		// caller does not attempt fallback for these; a broken
		// transport won't get better by switching wires.
		//
		// HTTP-status errors from the server (400/404) are surfaced
		// through the HTTPStatusError type — those get classified
		// below.
		if httpErr, ok := callErr.(*HTTPStatusError); ok {
			// 404 on server/discover is the canonical "not stateless"
			// signal under SEP-2575 (and our own legacy dispatcher's
			// behavior, which returns -32601 in a 200 body — also
			// handled below).
			if httpErr.StatusCode == 404 {
				return nil, true, nil
			}
		}
		return nil, false, callErr
	}
	if resp.Error != nil {
		// -32601 method-not-found in a 200 body: legacy server
		// confirming it does not speak server/discover. Fall back.
		if resp.Error.Code == core.ErrCodeMethodNotFound {
			return nil, true, nil
		}
		// Any other JSON-RPC error from server/discover is genuinely
		// fatal — the server speaks the wire (got far enough to emit
		// a JSON-RPC envelope) but rejected the call for a non-recoverable
		// reason (invalid _meta, version mismatch, etc.).
		return nil, false, fmt.Errorf("server/discover failed: %s", resp.Error.Message)
	}
	var dr discoverResult
	if err := json.Unmarshal(resp.Result, &dr); err != nil {
		return nil, false, fmt.Errorf("server/discover returned malformed result: %w", err)
	}
	return &dr, false, nil
}

// discoverResult mirrors stateless.DiscoverResult shape. Defined locally
// so client/ does not import server/stateless/ (the package boundary
// runs the other direction; client only knows wire types in core/).
type discoverResult struct {
	SupportedVersions []string                `json:"supportedVersions"`
	Capabilities      core.ServerCapabilities `json:"capabilities"`
	ServerInfo        core.ServerInfo         `json:"serverInfo"`
}

// buildRequestMeta builds the per-request _meta envelope the SEP-2575
// stateless wire requires. The clientCapabilities sub-field mirrors what
// the legacy initialize handshake advertises — same shape, additive
// across both wires.
func (c *Client) buildRequestMeta() map[string]any {
	return map[string]any{
		core.MetaKeyProtocolVersion: core.DraftProtocolVersion2026V1,
		core.MetaKeyClientInfo: map[string]any{
			"name":    c.info.Name,
			"version": c.info.Version,
		},
		core.MetaKeyClientCapabilities: c.computeClientCapabilities(),
	}
}

// computeClientCapabilities returns the ClientCapabilities object the
// client advertises on every stateless request. Mirrors the structure
// the legacy initialize handshake populates so both wires see the same
// shape downstream.
func (c *Client) computeClientCapabilities() core.ClientCapabilities {
	caps := core.ClientCapabilities{}
	if c.samplingHandler != nil {
		caps.Sampling = &struct{}{}
	}
	if c.elicitationHandler != nil {
		caps.Elicitation = &core.ElicitationCap{
			Form: &core.ElicitationFormCap{},
		}
		if c.elicitationURLSupport {
			caps.Elicitation.URL = &core.ElicitationURLCap{}
		}
	}
	if c.rootsHandler != nil {
		caps.Roots = &core.RootsCap{ListChanged: true}
	}
	if c.fileInputs {
		caps.FileInputs = &struct{}{}
	}
	if len(c.extensions) > 0 {
		caps.Extensions = make(map[string]core.ClientExtensionCap, len(c.extensions))
		for id, cap := range c.extensions {
			caps.Extensions[id] = cap
		}
	}
	return caps
}

// wrapParamsForStatelessWire decorates outgoing call params with the
// SEP-2575 _meta envelope. If params is nil/empty, returns a bare
// {"_meta": ...} object; if it's a struct/map, the envelope is merged
// into the params map alongside whatever the caller passed.
//
// Returns the params unchanged when c.useStatelessWire is false — every
// rawCall path is safe to wrap unconditionally.
func (c *Client) wrapParamsForStatelessWire(params any) any {
	if !c.useStatelessWire {
		return params
	}
	meta := c.buildRequestMeta()
	if params == nil {
		return map[string]any{"_meta": meta}
	}
	// If the caller already supplied a map, merge meta in. Otherwise
	// marshal-then-unmarshal to expose the field set so the envelope
	// joins cleanly.
	switch p := params.(type) {
	case map[string]any:
		// Don't clobber a caller-supplied _meta — they may have set
		// per-extension keys. Merge our required sub-fields in instead.
		mergeMetaInto(p, meta)
		return p
	default:
		raw, err := json.Marshal(p)
		if err != nil {
			return params // best effort; underlying call will surface the marshal error
		}
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			return params
		}
		if obj == nil {
			obj = make(map[string]any)
		}
		mergeMetaInto(obj, meta)
		return obj
	}
}

// mergeMetaInto adds our required SEP-2575 _meta sub-fields to params,
// preserving any caller-supplied _meta keys (extension namespaces etc.).
func mergeMetaInto(params, meta map[string]any) {
	existing, _ := params["_meta"].(map[string]any)
	if existing == nil {
		existing = make(map[string]any)
	}
	for k, v := range meta {
		if _, present := existing[k]; !present {
			existing[k] = v
		}
	}
	params["_meta"] = existing
}

// captureServerExtensions records server-advertised extensions from a
// stateless-wire discover result so subsequent ServerSupportsExtension
// calls work the same way they do post-legacy-initialize.
func (c *Client) captureServerExtensions(caps core.ServerCapabilities) {
	if len(caps.Extensions) == 0 {
		return
	}
	c.serverExtensions = make(map[string]json.RawMessage, len(caps.Extensions))
	for id, ext := range caps.Extensions {
		if raw, err := json.Marshal(ext); err == nil {
			c.serverExtensions[id] = raw
		}
	}
}

// tryDecodeJSONRPC attempts to read body as a JSON-RPC envelope. Returns
// the parsed response on success, nil on parse failure. Used by the
// streamable client transport on 4xx HTTP responses (SEP-2575 returns
// JSON-RPC errors in 400 / 404 bodies; legacy never does).
func tryDecodeJSONRPC(body []byte) *core.Response {
	if len(body) == 0 {
		return nil
	}
	var resp core.Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	// Must look like a JSON-RPC envelope — either a result or an error
	// alongside jsonrpc="2.0". A 4xx HTML body, for instance, will
	// unmarshal into an empty Response and we reject it here.
	if resp.JSONRPC != "2.0" || (resp.Result == nil && resp.Error == nil) {
		return nil
	}
	return &resp
}
