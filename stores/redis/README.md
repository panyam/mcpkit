# stores/redis — shared Redis adapter for mcpkit

Generic Redis primitives + the capability-shaped Pattern B transport. This module is **events-free** — nothing here references `events.Event`, so non-events surfaces (capability-shaped notifications, future request relays, etc.) can adopt without dragging in the events SDK.

What lives here:

| Symbol | Purpose |
|---|---|
| `Options` | Shared client + channel-prefix + logger + quota TTL + Bus-internal origin marker. Used by every Redis-backed mcpkit primitive. |
| `CapabilityBus` | `(method, params)`-typed Pattern B transport. Implements `server.NotificationRelay`; carries `tools/list_changed` / `resources/list_changed` / `prompts/list_changed` / `resources/updated`. |
| `CapabilityBusOptions` | Minimal config for `CapabilityBus` (Client + ChannelPrefix). |
| `Codec[T any]` | Generic wire-format seam. Implementations encode/decode any T. |
| `JSONCodec[T any]` | Default JSON-over-the-wire implementation. |
| `DefaultChannelPrefix` (`"mcpkit"`), `DefaultQuotaTTL` | Shared defaults applied via `Options.WithDefaults()`. The events SDK overrides `DefaultChannelPrefix` with its own `EventsChannelPrefix` (`"mcpkit.events"`) for back-compat of its existing channel naming; CapabilityBus and other root primitives use the neutral `"mcpkit"` namespace. |
| `redistest.NewClient(t)` | Miniredis-backed test client constructor. Importable from any module's `*_test.go`. |

What does NOT live here:

- The events-typed `Bus` (events SDK's `experimental/ext/events/stores/redis/`) — events-coupled because it owns the events.Event wire format.
- `QuotaStore` implementation — currently in the events SDK because the `Quota` interface it satisfies is defined there. May lift in a future cleanup.

See [`docs/MULTI_REPLICA.md`](../../docs/MULTI_REPLICA.md) for the full multi-replica architecture, per-surface flows, and adopter recipes. Issue 770 tracked the lift from `experimental/ext/events/stores/redis/`.

## Usage — CapabilityBus

```go
import (
    "github.com/redis/go-redis/v9"
    "github.com/panyam/mcpkit/server"
    redisstore "github.com/panyam/mcpkit/stores/redis"
)

cli := redis.NewClient(&redis.Options{Addr: "redis:6379"})

mux := server.NewNotificationRouter().
    Handle("notifications/tools/list_changed",     server.NewCapabilityBroadcastReceiver(srv)).
    Handle("notifications/resources/list_changed", server.NewCapabilityBroadcastReceiver(srv)).
    Handle("notifications/prompts/list_changed",   server.NewCapabilityBroadcastReceiver(srv)).
    Handle("notifications/resources/updated",      server.NewResourcesUpdatedReceiver(srv))

bus, _ := redisstore.NewCapabilityBus(redisstore.CapabilityBusOptions{Client: cli}, mux)
defer bus.Close()
_ = bus.Subscribe(ctx,
    "notifications/tools/list_changed",
    "notifications/resources/list_changed",
    "notifications/prompts/list_changed",
    "notifications/resources/updated",
)
go bus.Run(ctx)

srv := server.NewServer(info, server.WithNotificationRelay(bus))
```

## Test infrastructure

Adopters writing tests that need a Redis client (miniredis or real) import `stores/redis/redistest`:

```go
import "github.com/panyam/mcpkit/stores/redis/redistest"

func TestThing(t *testing.T) {
    cli := redistest.NewClient(t)
    // cli is a miniredis-backed *redis.Client; cleanup auto-registered with t.Cleanup
}
```

Set `MCPKIT_EVENTS_TEST_REDIS_ADDR=host:port` to run the same tests against a real Redis.
