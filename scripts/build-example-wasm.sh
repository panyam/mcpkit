#!/usr/bin/env bash
# Builds an example's server for the browser: GOOS=js GOARCH=wasm, plus a
# page and Go's wasm_exec.js, into <example>/web/dist/.
#
#   scripts/build-example-wasm.sh examples/getting-started ./server
#   scripts/build-example-wasm.sh examples/tasks-v2 ./web
#
# The page is the example's own web/index.html and web/worker.js when it has
# them, and the shared playground in examples/common/web otherwise (#1714).
#
# web/dist/ is gitignored. Never commit server.wasm (scripts/check-no-binaries.sh).
set -euo pipefail
dir="$(cd "$1" && pwd)"
pkg="${2:-./server}"
out="$dir/web/dist"
mkdir -p "$out"
(cd "$dir" && GOOS=js GOARCH=wasm go build -trimpath -ldflags='-s -w' -o "$out/server.wasm" "$pkg")
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$out/"
page="$dir/web"
[ -f "$page/index.html" ] || page="$(cd "$(dirname "${BASH_SOURCE[0]}")/../examples/common/web" && pwd)"
cp "$page/index.html" "$page/worker.js" "$out/"
[ -f "$dir/web/playground.json" ] && cp "$dir/web/playground.json" "$out/"
size=$(wc -c <"$out/server.wasm" | tr -d ' ')
gz=$(gzip -9 -c "$out/server.wasm" | wc -c | tr -d ' ')
echo "built $out (server.wasm $((size / 1024 / 1024)) MB, $((gz / 1024 / 1024)) MB gzipped)"
