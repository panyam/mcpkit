// options.go — generic Redis adapter configuration shared by every
// mcpkit primitive that talks to Redis pubsub or key-value:
//
//   - stores/redis.CapabilityBus (notification relay, this package)
//   - experimental/ext/events/stores/redis.Bus (events-typed Bus)
//   - experimental/ext/events/stores/redis.QuotaStore (events quota counters)
//
// Nothing in this file is events-typed; the wire-format Codec the
// events SDK uses to encode events.Event is defined alongside the
// events SDK's Bus (under experimental/ext/events/stores/redis/) so
// the root adapter stays events-free.
package redisstore

import (
	"time"

	"github.com/redis/go-redis/v9"
)

// DefaultChannelPrefix is the project-wide neutral namespace under
// which root mcpkit Redis primitives (CapabilityBus today) organize
// their channel names — "<prefix>.broadcast.<method>" / etc.
//
// The events SDK's Bus uses its own events-scoped default
// (experimental/ext/events/stores/redis.EventsChannelPrefix =
// "mcpkit.events") so adopters' existing deployments keep their
// per-event-name channels under the same "mcpkit.events.*" pattern.
// Adopters who want a different prefix set Options.ChannelPrefix
// explicitly.
const DefaultChannelPrefix = "mcpkit"

// DefaultQuotaTTL is the default leak-survival window for
// QuotaStore-managed counter keys. Long enough that a legitimate
// slow Reserve → Release loop never trips it; short enough that a
// leaked Reserve (caller crashed before releasing) drops within a
// typical operator shift. Sliding — every Reserve refreshes the
// TTL on the counter key, so active counters never expire under
// load.
const DefaultQuotaTTL = time.Hour

// Options bundles the generic configuration every Redis-backed mcpkit
// primitive accepts: the shared *redis.Client, the channel prefix
// (namespacing), the logger, the quota TTL, and the Bus-internal
// self-publish marker.
//
// Wire-format codecs are NOT here — the events SDK's Bus carries its
// own Codec (event-typed) on its struct rather than on Options, so
// the root stores/redis package can stay events-free.
//
// Client is required; every other field has a working default
// applied by WithDefaults.
type Options struct {
	// Client is the Redis client to use for PUBLISH / SUBSCRIBE /
	// EVAL. Every Pattern B leg of the deployment MUST be
	// configured against the same Redis deployment. The caller owns
	// the client's lifecycle — mcpkit primitives do not Close() it.
	Client *redis.Client

	// ChannelPrefix is the namespace prefix for Redis channel names.
	// Default: DefaultChannelPrefix. Override for multi-tenant
	// deployments running multiple isolated mcpkit stacks against
	// one Redis cluster.
	ChannelPrefix string

	// Logger emits operational messages (subscriber reconnect, decode
	// failure, etc.). Default: a no-op logger. Wire log.Printf or a
	// structured logger when debugging multi-replica setups.
	Logger func(format string, args ...any)

	// QuotaTTL is the sliding-window TTL the QuotaStore applies to
	// each counter key on every Reserve. Default DefaultQuotaTTL (1
	// hour). A leaked Reserve (caller crashed before Release) drops
	// after this many seconds of inactivity; active counters never
	// expire under load because EXPIRE is refreshed on every Reserve.
	// Set to 0 to use the default; negative values are invalid and
	// will produce an error from NewQuotaStore.
	QuotaTTL time.Duration

	// SkipOriginID is Bus-internal: when non-empty, the Subscriber
	// (events SDK side) drops messages whose origin marker matches
	// this value. The events SDK's Bus constructor wires this to its
	// colocated Publisher's marker so a replica's own publishes
	// don't double-fire its local handlers. Direct users of
	// Publisher / Subscriber (without Bus) leave this empty and get
	// raw messages with no self-publish dedup — intentional; the
	// recommended path is Bus.
	SkipOriginID string
}

// WithDefaults returns a copy of opts with zero-valued fields filled
// in from package defaults. Consumers (CapabilityBus, the events SDK
// Bus, QuotaStore) call this once at construction so the rest of the
// implementation can assume non-nil Logger / ChannelPrefix without
// scattering nil checks.
func (o Options) WithDefaults() Options {
	if o.ChannelPrefix == "" {
		o.ChannelPrefix = DefaultChannelPrefix
	}
	if o.Logger == nil {
		o.Logger = func(string, ...any) {}
	}
	if o.QuotaTTL == 0 {
		o.QuotaTTL = DefaultQuotaTTL
	}
	return o
}

// QuotaKeyFor returns the Redis key holding the counter for the given
// (principal, suffix). Lives under "<ChannelPrefix>.quota.<...>" so
// the logical namespace stays consistent across pubsub channels and
// quota keys.
func (o Options) QuotaKeyFor(principal, suffix string) string {
	return o.ChannelPrefix + ".quota." + principal + "." + suffix
}

// ChannelFor returns the Redis channel name with the given suffix,
// namespaced by ChannelPrefix. The events SDK's Bus uses event name
// as the suffix; CapabilityBus computes its own channel name with a
// fixed "broadcast." infix.
func (o Options) ChannelFor(suffix string) string {
	return o.ChannelPrefix + "." + suffix
}
