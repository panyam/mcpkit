package skills

import (
	"encoding/json"
)

// DecodeListChangedNotification extracts the optional ext/skills
// PathsChangedPayload from a notifications/resources/list_changed
// params object. Returns (payload, true) when the params carry the
// payload under _meta[MetaKeyPathsChanged]; returns (zero, false) for
// everything else — bare list_changed notifications, malformed
// payloads, or notifications from non-mcpkit servers.
//
// The params argument accepts anything the standard
// client.WithNotificationCallback produces (map[string]any from
// generic unmarshal, json.RawMessage from custom transports). Internal
// JSON round-trip handles both shapes without forcing callers to
// reason about types.
//
// Typical use from a client.WithNotificationCallback handler:
//
//	client.WithNotificationCallback(func(method string, params any) {
//	    if method != "notifications/resources/list_changed" {
//	        return
//	    }
//	    payload, ok := skills.DecodeListChangedNotification(params)
//	    if !ok {
//	        // Bare list_changed from a non-mcpkit server, or empty
//	        // params — re-read everything.
//	        return
//	    }
//	    for path, entry := range payload.Paths {
//	        switch entry.Action {
//	        case skills.ChangeActionDeleted:
//	            // prune local cache entry for `path`
//	        default:
//	            // re-fetch and re-verify digest
//	        }
//	    }
//	})
//
// Subscribers that compare entry.Digest against a cached digest can
// skip re-fetches entirely when the content already matches what they
// have; treat absent Digest as "fetch to find out."
func DecodeListChangedNotification(params any) (PathsChangedPayload, bool) {
	if params == nil {
		return PathsChangedPayload{}, false
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return PathsChangedPayload{}, false
	}
	var envelope struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return PathsChangedPayload{}, false
	}
	payloadRaw, ok := envelope.Meta[MetaKeyPathsChanged]
	if !ok {
		return PathsChangedPayload{}, false
	}
	var payload PathsChangedPayload
	if err := json.Unmarshal(payloadRaw, &payload); err != nil {
		return PathsChangedPayload{}, false
	}
	return payload, true
}
