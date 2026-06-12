package redisstore

import (
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/panyam/mcpkit/experimental/ext/events"
)

// DefaultChannelPrefix is the default namespace under which event
// channels are organized. Per-event channel name is
// "<prefix>.<event.Name>" — see channelFor.
const DefaultChannelPrefix = "mcpkit.events"

// DefaultQuotaTTL is the default leak-survival window for
// QuotaStore-managed counter keys. Long enough that a legitimate
// slow Reserve → Release loop never trips it; short enough that a
// leaked Reserve (caller crashed before releasing) drops within a
// typical operator shift. Sliding — every Reserve refreshes the
// TTL on the counter key, so active counters never expire under
// load.
const DefaultQuotaTTL = time.Hour

// Options bundles configuration for both Publisher and Subscriber.
// Client is required; everything else has a working default.
type Options struct {
	// Client is the Redis client to use for PUBLISH (Publisher) and
	// SUBSCRIBE (Subscriber). Both ends MUST be configured against
	// the same Redis deployment for the cross-replica fanout story
	// to work. The caller owns the client's lifecycle — Publisher
	// and Subscriber do not Close() it.
	Client *redis.Client

	// ChannelPrefix is the namespace prefix for per-event channels.
	// Default: DefaultChannelPrefix ("mcpkit.events"). Override for
	// multi-tenant deployments running multiple isolated demo stacks
	// against one Redis cluster.
	ChannelPrefix string

	// Codec encodes/decodes events.Event to/from the wire bytes
	// published on Redis. Default: JSONCodec. Implementations are
	// pluggable so deployments that care about wire-format efficiency
	// (protobuf, msgpack) can swap in without forking the sub-module.
	Codec Codec

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

	// skipOriginID is Bus-internal: when non-empty, Subscriber drops
	// messages whose origin marker matches this value. Wired by Bus
	// to its colocated Publisher's marker so a replica's own
	// publishes don't double-fire its local handlers. Direct users
	// of Publisher / Subscriber (without Bus) get raw messages with
	// no self-publish dedup — that's intentional; the recommended
	// path is Bus.
	skipOriginID string
}

// withDefaults returns a copy of opts with zero-valued fields filled
// in from package defaults. Used by NewPublisher and NewSubscriber so
// the rest of the implementation can assume non-nil Codec / Logger /
// ChannelPrefix without scattering nil checks.
func (o Options) withDefaults() Options {
	if o.ChannelPrefix == "" {
		o.ChannelPrefix = DefaultChannelPrefix
	}
	if o.Codec == nil {
		o.Codec = JSONCodec{}
	}
	if o.Logger == nil {
		o.Logger = func(string, ...any) {}
	}
	if o.QuotaTTL == 0 {
		o.QuotaTTL = DefaultQuotaTTL
	}
	return o
}

// quotaKeyFor returns the Redis key holding the counter for the given
// (principal, eventName). Lives under "<ChannelPrefix>.quota.<...>"
// so the events lib's logical namespace stays consistent across
// pubsub channels and quota keys.
func (o Options) quotaKeyFor(principal, eventName string) string {
	return o.ChannelPrefix + ".quota." + principal + "." + eventName
}

// channelFor returns the Redis channel name an event with the given
// Name publishes/subscribes on, namespaced by ChannelPrefix.
func (o Options) channelFor(eventName string) string {
	return o.ChannelPrefix + "." + eventName
}

// Codec is the seam between events.Event and the bytes carried over
// Redis pubsub. Implementations MUST be symmetric: Decode(Encode(e))
// reconstructs an Event semantically equivalent to e. The default
// JSONCodec round-trips every Event field (including the spec
// follow-on _meta map) without loss.
//
// Encode errors abort the publish (Emit returns the error). Decode
// errors are logged and the message dropped — a corrupt wire body
// SHOULD NOT take the subscriber goroutine down.
type Codec interface {
	Encode(events.Event) ([]byte, error)
	Decode([]byte) (events.Event, error)
}

// JSONCodec is the default Codec — encoding/json over the wire. Field
// names match events.Event's struct tags so cross-process round-trip
// works even if the writer and reader pin different mcpkit minor
// versions, as long as Event's wire shape stays additive.
type JSONCodec struct{}

func (JSONCodec) Encode(e events.Event) ([]byte, error) {
	return json.Marshal(e)
}

func (JSONCodec) Decode(b []byte) (events.Event, error) {
	var e events.Event
	if err := json.Unmarshal(b, &e); err != nil {
		return events.Event{}, err
	}
	return e, nil
}
