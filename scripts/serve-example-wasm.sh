#!/usr/bin/env bash
# Builds an example for the browser and serves it on localhost.
#
#   scripts/serve-example-wasm.sh examples/getting-started ./server 8788
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
"$here/build-example-wasm.sh" "$1" "${2:-./server}"
port="${3:-8788}"
echo "open http://localhost:$port/"
exec python3 -m http.server "$port" --bind 127.0.0.1 -d "$(cd "$1" && pwd)/web/dist"
