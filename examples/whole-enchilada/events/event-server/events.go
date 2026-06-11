// Package main implements the event-server tier of the whole-enchilada
// demo. It hosts the MCP Events extension, receives events from the
// push-server tier over HTTP, and fans out to subscribers via push /
// poll / webhook.
//
// Stage 1 deliberately runs with in-memory stores. Stages 2/3 add
// Keycloak (multi-tenant) and Postgres + Redis (multi-replica) without
// changing this directory layout.
package main

// ChatMessageData is the cursored event payload. Synthetic chat events
// fed by the push-server; semantically equivalent to a Discord /
// Slack-style message but without any third-party integration.
//
// Tenant tags the event for stage-2 multi-tenant isolation. The
// push-server stamps it at emission; the event-server's tenantMatchFunc
// only delivers to subscribers whose Claims.Tenant matches. Empty
// Tenant means "deliver to anyone" — the stage-1 single-tenant path
// keeps working unmodified.
type ChatMessageData struct {
	Tenant    string `json:"tenant,omitempty" jsonschema:"description=Tenant the event is scoped to. Empty = deliver to all subscribers regardless of tenant."`
	Channel   string `json:"channel" jsonschema:"description=Logical chat channel name."`
	Sender    string `json:"sender" jsonschema:"description=Username of the message author."`
	Text      string `json:"text" jsonschema:"description=Message body."`
	Timestamp string `json:"ts" jsonschema:"description=ISO 8601 timestamp,format=date-time"`
}

// PresenceChangedData is the cursorless event payload. Presence is
// ephemeral — subscribers can't replay missed transitions, only see
// live updates. The source is cursorless and emits cursor: null.
//
// Tenant: same semantics as ChatMessageData.Tenant — controls per-event
// delivery scoping in stage-2.
type PresenceChangedData struct {
	Tenant    string `json:"tenant,omitempty" jsonschema:"description=Tenant the event is scoped to. Empty = deliver to all subscribers regardless of tenant."`
	User      string `json:"user" jsonschema:"description=Username whose presence changed."`
	State     string `json:"state" jsonschema:"description=Presence state,enum=online,enum=away,enum=offline"`
	Timestamp string `json:"ts" jsonschema:"description=ISO 8601 timestamp,format=date-time"`
}
