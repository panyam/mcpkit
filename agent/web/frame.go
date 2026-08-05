package web

import (
	"encoding/json"

	"github.com/panyam/mcpkit/agent/host"
	agentwebv1 "github.com/panyam/mcpkit/agent/web/gen/go/mcpkit/agentweb/v1"
)

// eventToFrame projects a HostEvent onto its wire Frame: kind is the
// discriminator, payload is the event's own JSON — json.Marshal(HostEvent). It
// is a 1:1 projection of the A2 wire-serializable event, never a per-kind proto
// schema (agent/CONSTRAINTS A2 forbids a translation layer, and the web wire
// projection MUST be a mapping).
//
// A few HostEvent kinds still carry live pointers that json.Marshal can reject
// (the Failover inside a command result contains funcs; TaskStatus/Task are the
// task events) — that is the already-filed issue 994. On a marshal failure the
// frame carries a minimal {kind} payload so one un-serializable event never
// stalls the stream; the client still learns the event happened by its kind.
func eventToFrame(ev host.HostEvent) *agentwebv1.Frame {
	payload := marshalOrMinimal(ev, ev.Kind)
	return &agentwebv1.Frame{Kind: string(ev.Kind), Payload: payload}
}

// marshalOrMinimal returns json.Marshal(v), or a minimal {kind, _note} document
// when v carries a non-serializable field (issue 994). It never returns an
// error: the stream and command responses must degrade, not fail, on the long
// tail of not-yet-snapshotted payloads.
func marshalOrMinimal(v any, kind host.HostEventKind) []byte {
	if b, err := json.Marshal(v); err == nil {
		return b
	}
	b, _ := json.Marshal(struct {
		Kind host.HostEventKind `json:"Kind"`
		Note string             `json:"_note"`
	}{Kind: kind, Note: "issue 994: non-serializable payload omitted"})
	return b
}
