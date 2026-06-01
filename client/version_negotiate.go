package client

import (
	"github.com/panyam/mcpkit/core"
)

// SEP-2575 §protocol-version-header: when a server rejects a request with
// JSON-RPC error code -32001 / -32004 and returns `data.supported: [...]`,
// the client SHOULD pick a mutually supported version and retry. This file
// holds the pure decision helpers; the retry orchestration lives in
// client.go::rawCallWithContext.

// isUnsupportedVersionError reports whether resp is a server rejection
// that should trigger version-retry. The signal is the presence of a
// non-empty `data.supported` string array on a -32001 or -32004 error —
// both codes appear in the wild (mcpkit's own server uses -32004, the
// upstream conformance scenario emits -32001), and the semantic
// discriminator is the supported-list payload, not the code itself.
// HeaderMismatchData's -32001 responses don't carry `supported`, so
// the predicate naturally distinguishes them.
//
// Returns (true, picked) when the supported list intersects mcpkit's
// stateless-wire support set and a usable downgrade exists; otherwise
// (false, "").
func isUnsupportedVersionError(resp *rpcResponse) (retry bool, picked string) {
	if resp == nil || resp.Error == nil {
		return false, ""
	}
	if resp.Error.Code != core.ErrCodeHeaderMismatch &&
		resp.Error.Code != core.ErrCodeUnsupportedProtocolVersion {
		return false, ""
	}
	supported, ok := extractSupportedList(resp.Error.Data)
	if !ok || len(supported) == 0 {
		return false, ""
	}
	picked = pickSupportedVersion(supported, core.SupportedStatelessVersions)
	return picked != "", picked
}

// pickSupportedVersion returns the first version in clientSupported that
// also appears in serverSupported, or "" if the intersection is empty.
// Ordered by clientSupported so callers control preference (newest-first
// in mcpkit's case — see core.SupportedStatelessVersions).
func pickSupportedVersion(serverSupported, clientSupported []string) string {
	server := make(map[string]struct{}, len(serverSupported))
	for _, v := range serverSupported {
		server[v] = struct{}{}
	}
	for _, v := range clientSupported {
		if _, ok := server[v]; ok {
			return v
		}
	}
	return ""
}

// extractSupportedList pulls the `supported` field out of an error's
// structured data payload. Tolerates the two on-wire shapes the field
// arrives in: map[string]any (raw JSON decode) and []string (typed
// helpers that pre-parsed). Returns (nil, false) on any other shape.
func extractSupportedList(data any) ([]string, bool) {
	switch d := data.(type) {
	case map[string]any:
		raw, ok := d["supported"]
		if !ok {
			return nil, false
		}
		return toStringSlice(raw)
	case []string:
		return d, true
	}
	return nil, false
}

func toStringSlice(v any) ([]string, bool) {
	switch s := v.(type) {
	case []string:
		return s, true
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			str, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, str)
		}
		return out, true
	}
	return nil, false
}
