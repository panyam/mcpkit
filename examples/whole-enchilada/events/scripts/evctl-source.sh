#!/usr/bin/env bash
# Runtime source admin against the per-replica /admin/replicas/{idx}/ endpoints
# on the nginx frontdoor. Lets the operator add or remove a real Discord source
# on specific event-server replicas mid-demo.
#
# Usage: evctl-source.sh <add-discord|rm>
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/common.sh

ACTION="${1:?internal: evctl-source.sh needs add-discord|rm}"

TOKEN="${TOKEN:-}"
CHANNELS="${CHANNELS:-}"
REPLICAS="${REPLICAS:-1}"
TENANTS="${TENANTS:-asgard,babylon,camelot}"
NAME="${NAME:-discord.message}"
SOURCE="${SOURCE:-}"

case "$ACTION" in
    add-discord)
        if [ -z "$TOKEN" ] || [ -z "$CHANNELS" ] || [ -z "$REPLICAS" ]; then
            usage_for add-discord "TOKEN=<bot-token>" "CHANNELS=<id,id>" \
                "REPLICAS=<idx,idx>" "[TENANTS=<a,c>]" "[NAME=<source>]"
        fi
        exec go -C evctl run . sources add discord \
            --token="$TOKEN" --channels="$CHANNELS" --tenants="$TENANTS" \
            --replicas="$REPLICAS" --name="$NAME"
        ;;
    rm)
        if [ -z "$SOURCE" ] || [ -z "$REPLICAS" ]; then
            usage_for rm-source "SOURCE=<name>" "REPLICAS=<idx,idx>"
        fi
        exec go -C evctl run . sources rm "$SOURCE" --replicas="$REPLICAS"
        ;;
    *)
        echo "internal: unknown action '$ACTION'" >&2
        exit 2
        ;;
esac
