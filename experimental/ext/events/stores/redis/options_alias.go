// options_alias.go — events SDK aliases over the generic
// stores/redis primitives so existing call sites
// (NewBus(opts, receiver), Publisher / Subscriber tests, QuotaStore
// adopters) keep their familiar names.
//
// Adopters that want the generic types directly can import the root
// package as rootredis "github.com/panyam/mcpkit/stores/redis" and
// use rootredis.Codec[T] / rootredis.JSONCodec[T] / rootredis.Options.
package redisstore

import (
	"github.com/panyam/mcpkit/experimental/ext/events"
	rootredis "github.com/panyam/mcpkit/stores/redis"
)

// Options aliases the generic stores/redis Options. Provided for API
// continuity — existing call sites that say `redisstore.Options` (in
// the events SDK import path) keep working without rewiring.
type Options = rootredis.Options

// Codec aliases rootredis.Codec instantiated over events.Event — the
// events SDK's wire-format seam over Redis pubsub. Adopters writing
// custom codecs implement this interface against events.Event.
type Codec = rootredis.Codec[events.Event]

// JSONCodec aliases rootredis.JSONCodec instantiated over
// events.Event — the default events-typed JSON codec. Adopters that
// need a different wire format implement Codec directly.
type JSONCodec = rootredis.JSONCodec[events.Event]

// EventsChannelPrefix is the events SDK's default ChannelPrefix —
// "mcpkit.events". Events Bus channels are organized as
// "<EventsChannelPrefix>.<event.Name>" (e.g. "mcpkit.events.chat.message");
// events QuotaStore counters live under
// "<EventsChannelPrefix>.quota.<principal>.<eventName>".
//
// The events SDK's Bus / Publisher / Subscriber / QuotaStore
// constructors substitute this value when opts.ChannelPrefix is
// empty, so adopters' existing deployments keep their current channel
// namespace.
//
// Distinct from rootredis.DefaultChannelPrefix ("mcpkit"), which is
// what root-level primitives like CapabilityBus default to. The events
// SDK uses its own scoped prefix so non-events Redis channels (e.g.
// notification broadcasts) don't pile under "mcpkit.events.*".
const EventsChannelPrefix = "mcpkit.events"

// DefaultQuotaTTL re-exports the root constant.
const DefaultQuotaTTL = rootredis.DefaultQuotaTTL

// eventsDefaults substitutes the events-SDK-scoped ChannelPrefix when
// the caller left it empty, then applies the root's WithDefaults to
// fill in any remaining zero fields. Used by every events SDK
// constructor (NewBus, NewPublisher, NewSubscriber, NewQuotaStore)
// so they consistently default to the events-scoped prefix instead
// of the root's neutral "mcpkit".
func eventsDefaults(opts Options) Options {
	if opts.ChannelPrefix == "" {
		opts.ChannelPrefix = EventsChannelPrefix
	}
	return opts.WithDefaults()
}
