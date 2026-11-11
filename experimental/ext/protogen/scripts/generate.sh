#!/usr/bin/env bash
# Regenerate <dir>/gen with `buf generate`. The new output goes to a temp
# directory first and replaces gen/ only when buf succeeds, so a failed
# generate leaves the committed files in place (#1551).
#
#   generate.sh <dir containing buf.gen.yaml>
set -euo pipefail
dir="${1:?usage: generate.sh <dir containing buf.gen.yaml>}"
cd "$dir"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/protogen-gen.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT
buf generate -o "$tmp"
if [ ! -d "$tmp/gen" ]; then
    echo "generate.sh: buf generate succeeded but wrote no gen/ under $tmp" >&2
    exit 1
fi
rm -rf gen
mv "$tmp/gen" gen
