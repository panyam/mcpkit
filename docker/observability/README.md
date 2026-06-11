# observability/

LGTM-shaped local observability stack for mcpkit examples. Five
containers receiving OTLP and fanning out to a per-signal backend, all
unified under one Grafana UI.

## What runs

| Container | Image | Purpose | Ports |
|---|---|---|---|
| `otel-collector` | `otel/opentelemetry-collector-contrib:0.114.0` | Receives OTLP, splits by signal, forwards | `4317` (gRPC), `4318` (HTTP), `8888` (self-metrics) |
| `tempo` | `grafana/tempo:2.6.1` | Trace storage | `3200` (HTTP) |
| `loki` | `grafana/loki:3.2.1` | Log storage | `3100` (HTTP) |
| `mimir` | `grafana/mimir:2.14.0` | Metric storage (Prometheus-compatible) | `9009` (HTTP) |
| `grafana` | `grafana/grafana:11.3.1` | Single UI for all three signals | `3000` |

## Quick start

```
make -C docker up                 # bring the stack up
open http://localhost:3000        # Grafana — anonymous Admin, no login
make -C docker down               # tear it down
```

To point a mcpkit example at the stack, configure its OTel SDK to
export OTLP at `localhost:4317` (gRPC) or `localhost:4318` (HTTP).
[`examples/otel/stdout/`](../../examples/otel/stdout/) is the
reference — pass `--exporter=otlp` to its `serve` or `demo` target.

## What lights up today

- **Traces** (Tempo lane) — every mcpkit example with SEP-414 wired
  emits spans through the OTel Collector to Tempo. Search by service
  name in Grafana → Explore → Tempo. SEP-414 P1–P5 surfaces (server,
  client, dispatch spine) are already instrumented.

- **Logs** (Loki lane) — wired via `commonotel.SetupLogs` (issue
  668). Examples that adopt it ship `slog.*Context` records through
  the otelslog bridge → OTLP → Collector → Loki, with `trace_id` /
  `span_id` stamped automatically when the handler passes ctx.
  Grafana's Loki datasource (auto-provisioned with a `traceID`
  derived field) renders these as clickable pivots back to Tempo.
  `examples/otel/stdout/` is the reference adopter; the metrics
  half of issue 668 (Mimir lane) is the next adoption to land.
  MCP `notifications/message` is a separate, client-visible
  surface and continues to work independently.

- **Metrics** (Mimir lane) — wired via `commonotel.SetupMetrics`
  (issue 668 metrics half, pairs with the `core.MeterProvider` seam
  added in issue 7). Examples that adopt it emit four canonical
  instruments — `mcp.tool.calls`, `mcp.jsonrpc.errors`,
  `mcp.tool.duration` (ms), `mcp.sessions.active` — through the OTel
  meter adapter → OTLP → Collector → Mimir. Exemplars are stamped
  by default so Grafana panels link directly to the matching trace
  in Tempo. The bundled
  [`mcpkit — overview`](http://localhost:3000/d/mcpkit-overview)
  dashboard works for ANY example: pick the example from the
  `$service` dropdown. Per-example dashboards are an escape hatch
  for genuinely-bespoke metrics — see `examples/CONVENTIONS.md`
  § Grafana dashboards.

Shipping the full LGTM stack now means no rework when the empty lanes
fill in. The trade-off is ~6 YAML config files to maintain vs the
1-file Jaeger-only alternative.

## Grafana data sources

Auto-provisioned on container start:

- **Tempo** (default) — TraceQL queries; trace-to-logs and
  trace-to-metrics links pre-wired to the Loki / Mimir data sources.
- **Loki** — derived field extracts `traceID=<hex>` from log lines
  and links back to Tempo.
- **Mimir** — Prometheus query API; treats Mimir as the Prometheus
  data source type with `prometheusType: Mimir`.

No login required — `GF_AUTH_ANONYMOUS_ENABLED=true` +
`GF_AUTH_DISABLE_LOGIN_FORM=true` make Grafana a single-click open.

## Bringing data in

```bash
# OTLP gRPC (the default for OTel SDK in Go):
export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317
export OTEL_EXPORTER_OTLP_PROTOCOL=grpc

# OTLP HTTP (handy for curl or browser-side exporters):
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
```

mcpkit examples bake these defaults into their `--exporter=otlp`
mode; the env vars are documented here for non-mcpkit OTLP emitters.

## Troubleshooting

- **`Grafana: error opening datasource`** — the backend container is
  not up yet. Wait for `make logs` to show all five containers
  serving, then refresh the Grafana tab.
- **`OTLP connection refused`** — collector container isn't running.
  `make ps` to confirm, `make logs` to inspect, `make down && make up`
  to reset.
- **`unknown_service` in the trace UI** — the emitting process didn't
  set an OTel `service.name` Resource. mcpkit examples set it via
  `sdktrace.WithResource(...)`; the collector ALSO inserts a default
  `unknown_mcpkit_example` so unconfigured emitters still surface
  somewhere visible. See [issue 674][svcname] for the planned
  `ext/otel.WithServiceName` helper.
- **Port collision** (`port 3000 already in use`) — Grafana is the
  most likely victim. Stop any local Grafana / Jaeger / Prometheus
  on those ports, or edit the compose file to remap. The full port
  set is documented in the table above.

## Production note

This stack is sized for a developer laptop — single-binary modes,
local filesystem storage, no replication. It exists for demoing
mcpkit examples and validating SEP-414 wiring, not for serving
real workloads. A production deployment would split each backend
(Tempo, Loki, Mimir, Grafana) into its component services, back
storage with S3 / GCS, and run multiple replicas behind a
load balancer.

[svcname]: https://github.com/panyam/mcpkit/issues/674
