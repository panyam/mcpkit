// origin.go — redisstore-internal self-publish dedup.
//
// Pattern B with Redis pubsub round-trips a replica's own publishes
// back to its own Subscriber. Without filtering, the colocated
// receive loop would re-fire whatever the yield path already
// delivered locally (per-slot fanout, webhook POSTs, etc.).
//
// The fix is contained inside this package: Publisher stamps a
// per-instance origin marker onto event.Meta; Subscriber drops
// messages whose marker matches its colocated Publisher's; the
// marker is stripped from Meta before deliverFn fires so consumers
// of the events package never see this transport-internal plumbing.
//
// All symbols here are package-private. Adopters consume redisstore
// through the Bus (bus.go), which wires Publisher + Subscriber
// together with origin handling automatic and invisible.
package redisstore

// originMetaKey is the Event.Meta key under which Publisher stamps
// its per-instance origin marker. Underscore prefix follows the
// "internal/private" convention so this can't collide with any
// spec-defined `_meta` field.
const originMetaKey = "_mcpkit_redisstore_origin"

// originIDFromMeta returns the origin marker carried on event.Meta,
// or "" if absent / wrong-typed. Safe to call with a nil Meta.
func originIDFromMeta(meta map[string]any) string {
	if meta == nil {
		return ""
	}
	v, _ := meta[originMetaKey].(string)
	return v
}

// stripOriginIDFromMeta removes the origin marker from Meta so
// downstream consumers (delivery handlers, webhook receivers, stream
// subscribers) never see it. Returns the same map for chaining;
// nil-in, nil-out. Idempotent — safe to call when the marker is
// absent.
func stripOriginIDFromMeta(meta map[string]any) map[string]any {
	if meta == nil {
		return nil
	}
	delete(meta, originMetaKey)
	return meta
}

// stampOriginIDOnMeta returns a Meta map carrying originID under
// originMetaKey. Always returns a freshly-allocated map when
// injection happens, so callers passing the event's Meta in don't
// see their map mutated through aliasing — Event is passed by value
// but Meta is a reference type.
//
// An empty originID is a no-op: returns the input unchanged.
func stampOriginIDOnMeta(meta map[string]any, originID string) map[string]any {
	if originID == "" {
		return meta
	}
	cp := make(map[string]any, len(meta)+1)
	for k, v := range meta {
		cp[k] = v
	}
	cp[originMetaKey] = originID
	return cp
}
