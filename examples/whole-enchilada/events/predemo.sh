#!/usr/bin/env bash
# Clean slate (run ONCE before the demo): WIPE backend data, rebuild images,
# restart everything, open Keycloak + Grafana. Wiping the DB is deliberate —
# schema changes ship "fresh deploys only" (no migrations), so a stale volume
# can crash event-server on AutoMigrate.
#
# Shared by the whole-enchilada/events Makefile + justfile `predemo` recipes
# (both keep the `gen-compose` prereq; this script is the body). Run from the
# example directory. The backend-data wipe inlines the `clean-backends` recipe.
#
# Env (required, supplied by the caller):
#   BACKENDS_COMPOSE       path to docker/backends/docker-compose.yml
#   OBSERVABILITY_COMPOSE  path to docker/observability/docker-compose.yml
set -eu

echo "[predemo] tearing down events + observability..."
docker compose down 2>/dev/null || true
docker compose -f "${OBSERVABILITY_COMPOSE}" down 2>/dev/null || true
echo "[predemo] wiping backend data (fresh empty DB → AutoMigrate builds the current schema)..."
docker compose -f "${BACKENDS_COMPOSE}" down -v 2>/dev/null || true
rm -rf ../../../docker/backends/data/
echo "[predemo] starting backends + observability fresh..."
docker compose -f "${BACKENDS_COMPOSE}" up -d --wait
docker compose -f "${OBSERVABILITY_COMPOSE}" up -d --wait
echo "[predemo] building + starting events stack fresh..."
docker compose up -d --build
echo "[predemo] opening Keycloak admin + Grafana in browser..."
open http://localhost:8180 2>/dev/null || xdg-open http://localhost:8180 2>/dev/null || echo "  → http://localhost:8180 (admin/admin)"
open http://localhost:3000 2>/dev/null || xdg-open http://localhost:3000 2>/dev/null || echo "  → http://localhost:3000"
echo ""
echo "[predemo] ready. Walkthrough is now self-driving — open the windows and run commands as you go."
