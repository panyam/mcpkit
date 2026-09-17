#!/usr/bin/env bash
# Clean slate (run ONCE before the demo): WIPE the events stack's data, rebuild
# images, restart everything, open Keycloak + Grafana. Wiping is deliberate —
# schema changes ship "fresh deploys only" (no migrations), so a stale volume
# can crash event-server on AutoMigrate.
#
# Shared by the whole-enchilada/events Makefile + justfile `predemo` recipes
# (both keep the `gen-compose` prereq; this script is the body). Run from the
# example directory.
#
# It wipes the events stack's OWN named volumes, via `down -v` on the project.
# It deliberately does NOT touch docker/backends: before the compose merge this
# script ran `rm -rf ../../../docker/backends/data/`, which since the merge
# would destroy the Postgres that examples/agents depends on while leaving the
# events data it was actually aiming at untouched.
#
# Env (required, supplied by the caller):
#   COMPOSE                the `docker compose -f ... -f ...` invocation
#   OBSERVABILITY_COMPOSE  path to docker/observability/docker-compose.yml
#   AUTH_INTROSPECTION_URLS  realm introspection endpoints for the auth profile
set -eu

echo "[predemo] tearing down events (with volumes) + observability..."
${COMPOSE} --profile auth down -v 2>/dev/null || true
docker compose -f "${OBSERVABILITY_COMPOSE}" down 2>/dev/null || true

echo "[predemo] starting observability fresh..."
docker compose -f "${OBSERVABILITY_COMPOSE}" up -d --wait

echo "[predemo] building + starting the events stack fresh (empty DB → AutoMigrate builds the current schema)..."
OAUTH_INTROSPECTION_URLS="${AUTH_INTROSPECTION_URLS}" \
  ${COMPOSE} --profile auth up -d --build --wait --wait-timeout 300

echo "[predemo] opening Keycloak admin + Grafana in browser..."
open http://localhost:8180 2>/dev/null || xdg-open http://localhost:8180 2>/dev/null || echo "  → http://localhost:8180 (admin/admin)"
open http://localhost:3000 2>/dev/null || xdg-open http://localhost:3000 2>/dev/null || echo "  → http://localhost:3000"
echo ""
echo "[predemo] ready. Walkthrough is now self-driving — open the windows and run commands as you go."
