#!/usr/bin/env bash
# Builds a browser-ready example and serves its playground on localhost.
#
#   scripts/serve-example-wasm.sh examples/tasks-v2 [PORT]
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
"$here/build-example-wasm.sh" "$1"
port="${2:-8788}"
echo "open http://localhost:$port/  (add ?wire=session for the session wire)"
exec python3 -m http.server "$port" --bind 127.0.0.1 -d "$(cd "$1" && pwd)/web/dist"
