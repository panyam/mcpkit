// match.go holds the asgardware MatchFunc shared by every EventDef
// the event-server registers. The function reads the event's tenant
// tag from the JSON payload (Data.tenant) and compares it against the
// subscriber's tenant from claims (HookContext.Principal()'s tenant
// prefix). An event tagged for "asgard" delivers only to
// subscriptions whose Claims.Tenant == "asgard"; an untagged event
// (the stage-1 path) delivers to all subscribers.
//
// This MatchFunc is intentionally event-server-local. The events
// library's MatchFunc surface (experimental/ext/events/hooks.go) is
// the general-purpose plug; tenant filtering is an authorship policy,
// not a library concern, so it lives here next to the EventDef
// registrations.
package main

import (
	"encoding/json"

	"github.com/panyam/mcpkit/core"
	"github.com/panyam/mcpkit/experimental/ext/events"
)

// tenantTaggedEvent is the minimal shape we decode out of the event
// payload to read the tenant tag. Decoding into a small dedicated
// struct (rather than the full ChatMessageData / PresenceChangedData
// types) keeps the MatchFunc generic across event types — every event
// that ships a top-level "tenant" field gets the same isolation rule.
type tenantTaggedEvent struct {
	Tenant string `json:"tenant"`
}

// tenantMatchFunc returns true iff the subscriber's tenant matches the
// event's tenant tag, or the event carries no tenant tag (deliver to
// everyone — preserves stage-1 single-tenant behavior).
//
// On JSON decode failure the function returns false (no delivery). A
// malformed event payload is a push-server bug; better to drop the
// event than to leak it cross-tenant.
func tenantMatchFunc(ctx events.HookContext, event events.Event, _ map[string]any) bool {
	var tagged tenantTaggedEvent
	if err := json.Unmarshal(event.Data, &tagged); err != nil {
		return false
	}
	if tagged.Tenant == "" {
		return true
	}
	return tagged.Tenant == core.TenantOf(ctx.Principal())
}
