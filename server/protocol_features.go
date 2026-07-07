package server

import "github.com/panyam/mcpkit/core"

// This file is the single source of truth for protocol-version negotiation and
// for which version-gated behaviors are active on a negotiated version. When a
// new SEP gates behavior on the negotiated protocol version, add a field to
// ProtocolFeatures and set it in featuresForVersion — do NOT scatter fresh
// `negotiatedVersion == "..."` string comparisons across the dispatch and
// transport code. Centralizing keeps the legacy and stateless wires from
// drifting (a recurring parity trap) and gives contributors one table to read.

// protocolVersions returns the protocol versions this dispatcher accepts —
// the operator-configured set (WithSupportedVersions) when non-empty, else the
// package-level default. This is the single reader that negotiation, the
// MCP-Protocol-Version header check, and the discover advertise consult, so a
// per-server override applies uniformly (issue 419).
func (d *Dispatcher) protocolVersions() []string {
	if len(d.configuredVersions) > 0 {
		return d.configuredVersions
	}
	return supportedProtocolVersions
}

// preferredProtocolVersion is the version the server offers when a client
// requests one it does not support. Per the MCP version-negotiation handshake
// this SHOULD be the latest version the server supports, which is the first
// entry in supportedProtocolVersions.
func preferredProtocolVersion(versions []string) string {
	return versions[0]
}

// negotiateProtocolVersion implements the MCP initialize version handshake
// (spec 2025-03-26 §Version Negotiation) over the given supported set: if the
// client's requested version is in the set, echo it back; otherwise respond
// with the set's preferred (first) version and let the client decide whether to
// proceed on that version or disconnect. This replaces the older behavior of
// rejecting an unsupported version outright. The set is the server's configured
// versions (WithSupportedVersions) or the package default — see
// Dispatcher.protocolVersions.
func negotiateProtocolVersion(versions []string, requested string) string {
	for _, sv := range versions {
		if sv == requested {
			return sv
		}
	}
	return preferredProtocolVersion(versions)
}

// ProtocolFeatures describes the version-gated behaviors active for a given
// negotiated protocol version. Fields are additive: a zero value means "no
// version-gated behavior," which is the correct default for every dated
// release before the draft line.
type ProtocolFeatures struct {
	// RoutingHeaderValidation enforces SEP-2243 §Server Validation: the
	// Mcp-Method header (always) and Mcp-Name header (for name-carrying
	// methods) MUST match the request body, or the server rejects with
	// HTTP 400 + JSON-RPC -32020.
	RoutingHeaderValidation bool

	// StatelessMetaRequired enforces SEP-2575: on the draft line the
	// initialize handshake is gone and every request MUST carry the
	// per-request _meta envelope (protocolVersion / clientInfo /
	// clientCapabilities); missing _meta is rejected with -32602. Servers can
	// opt out of the strict check with WithAllowLegacyOnDraft (see
	// Dispatcher.allowLegacyOnDraft), which is applied by the caller.
	StatelessMetaRequired bool
}

// featuresForVersion resolves the version-gated feature set for a negotiated
// protocol version. This is the one place to edit when a SEP starts (or stops)
// gating behavior on the protocol version.
func featuresForVersion(version string) ProtocolFeatures {
	switch version {
	case core.DraftProtocolVersion2026V1: // 2026-07-28 (draft / stateless line)
		return ProtocolFeatures{
			RoutingHeaderValidation: true,
			StatelessMetaRequired:   true,
		}
	default:
		return ProtocolFeatures{}
	}
}

// protocolFeatures returns the version-gated feature set for this dispatcher's
// currently negotiated protocol version.
func (d *Dispatcher) protocolFeatures() ProtocolFeatures {
	return featuresForVersion(d.negotiatedVersion)
}
