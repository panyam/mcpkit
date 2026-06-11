# docker/

Local docker-compose stacks for mcpkit development. Each subdirectory
is one independent stack with its own `Makefile`; `cd` into the one
you want and run `make up`:

- [`observability/`](./observability/) — OpenTelemetry Collector +
  Tempo + Loki + Mimir + Grafana. Receives OTLP from any mcpkit
  example with `--exporter=otlp` (or any OTLP-aware emitter) and
  unifies traces / logs / metrics under a single Grafana UI at
  `http://localhost:3000`.
- [`backends/`](./backends/) — Keycloak (identity), Postgres
  (relational state), Redis (cache + pub/sub). Shared across any
  example that needs auth + persistence.

The stacks are independent — bring up whichever subset an example
needs.

## Quick start

```
cd docker/observability && make up    # OTel + Tempo + Loki + Mimir + Grafana
cd docker/backends      && make up    # Keycloak + Postgres + Redis
```

Each stack exposes the same target set: `up` / `down` / `logs` /
`build` / `ps` / `config`. The default goal is `up`.

## Adding a new stack

1. Create `docker/<name>/docker-compose.yml`.
2. Mount any config files from `docker/<name>/<service>/...` (one
   subdirectory per service is the convention; see
   `observability/collector/`, `observability/tempo/`, etc.).
3. Add a `docker/<name>/Makefile` mirroring the `up` / `down` /
   `logs` / `build` / `ps` / `config` target set (copy from
   `observability/Makefile`).
4. Add a `docker/<name>/README.md` describing what the stack does +
   ports + datasource credentials.

## Conventions

- **Pin image versions.** `make up` should produce the same containers
  every time. Schema bumps (Tempo / Loki / Mimir) need an explicit
  version bump in the compose file.
- **Sized for a laptop.** Configs use single-binary modes and local
  filesystem storage. Production deployment is out of scope — these
  stacks exist for demoing mcpkit examples and validating SEP-414
  wiring, not for serving real workloads.
- **Stateless by default.** No volume mounts for data dirs — tearing
  the stack down (`make down`) wipes traces / logs / metrics. Add
  named volumes if a future stack needs persistence across restarts.
