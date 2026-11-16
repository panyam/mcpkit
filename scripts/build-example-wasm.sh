#!/usr/bin/env bash
# Builds a browser-ready example for the browser: its server for GOOS=js
# GOARCH=wasm, the shared playground page (examples/common/web), Go's
# wasm_exec.js and the example's web/playground.json, into <example>/web/dist/.
#
#   scripts/build-example-wasm.sh examples/tasks-v2
#
# The package to build is the manifest's "package", default ./web (#1719).
#
# web/dist/ is gitignored. Never commit server.wasm (scripts/check-no-binaries.sh).
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
dir="$(cd "$1" && pwd)"
manifest="$dir/web/playground.json"
[ -f "$manifest" ] || { echo "build-example-wasm: $dir has no web/playground.json" >&2; exit 1; }
pkg="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("package", "./web"))' "$manifest")"
out="$dir/web/dist"
mkdir -p "$out"
(cd "$dir" && GOOS=js GOARCH=wasm go build -trimpath -ldflags='-s -w' -o "$out/server.wasm" "$pkg")
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$out/"
cp "$here/../examples/common/web/index.html" "$here/../examples/common/web/worker.js" "$manifest" "$out/"
size=$(wc -c <"$out/server.wasm" | tr -d ' ')
gz=$(gzip -9 -c "$out/server.wasm" | wc -c | tr -d ' ')
echo "built $out (server.wasm $((size / 1024 / 1024)) MB, $((gz / 1024 / 1024)) MB gzipped)"
