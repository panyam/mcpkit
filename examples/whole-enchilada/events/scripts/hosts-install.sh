#!/usr/bin/env bash
# Appends the *.whole-enchilada hostnames to /etc/hosts, idempotently. The
# marker comment is what makes it reversible; hosts-uninstall matches on it.
# Requires sudo.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
. scripts/common.sh

MARKER="whole-enchilada hostnames"
HOSTS_NAMES="${HOSTS_NAMES:-nginx.whole-enchilada event-server.whole-enchilada event-server-1.whole-enchilada event-server-2.whole-enchilada event-server-3.whole-enchilada}"

if grep -q "$MARKER" /etc/hosts; then
    echo "[hosts] already installed; run $RUNNER hosts-uninstall first to refresh"
    exit 0
fi

sudo sh -c "printf '\n# $MARKER — managed by $RUNNER hosts-install\n127.0.0.1 $HOSTS_NAMES\n' >> /etc/hosts"
echo "[hosts] installed: $HOSTS_NAMES"
