# docker/

Local docker-compose stacks for mcpkit development. Each subdirectory
is one independent stack. Today there is one:

- [`observability/`](./observability/) — OpenTelemetry Collector +
  Tempo + Loki + Mimir + Grafana. Receives OTLP from any mcpkit
  example with `--exporter=otlp` (or any OTLP-aware emitter) and
  unifies traces / logs / metrics under a single Grafana UI at
  `http://localhost:3000`.

## Quick start

```
cd docker
make up      # bring up the default stack (observability)
make ps      # confirm containers are running
make logs    # tail logs (Ctrl-C to exit)
make down    # tear everything down
```

The target names (`up` / `down` / `logs` / `build` / `ps` / `config`)
stay short while there is one stack. When a second stack lands, the
plan is to rename to `obs-up` / `obs-down` / etc. and add the sibling
target set — small transition cost, avoids over-engineering
namespacing upfront.

To target a non-default stack, pass `STACK=`:

```
make STACK=observability up
```

## Adding a new stack

1. Create `docker/<name>/docker-compose.yml`.
2. Mount any config files from `docker/<name>/<service>/...` (one
   subdirectory per service is the convention; see
   `observability/collector/`, `observability/tempo/`, etc.).
3. Add a `docker/<name>/README.md` describing what the stack does +
   ports + datasource credentials.
4. If renaming the top-level targets to namespaced form is appropriate
   (two or more stacks now in tree), update `docker/Makefile`
   accordingly.

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
