#!/usr/bin/env bash
# @panyam/mcpkit-rooms (experimental/ext/rooms/ts) is versioned with the mcpkit
# release: the npm package at X.Y.Z is the page side of the bridge protocol
# the Go modules carry at vX.Y.Z. Its version is read from the root pin in
# experimental/ext/rooms/go.mod, which make bump-root sets.
#
#   scripts/rooms-ts-version.sh check [tag]   # CI, and the publish job with the tag
#   scripts/rooms-ts-version.sh set vX.Y.Z    # bump-root
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PKG="$ROOT/experimental/ext/rooms/ts/package.json"
GOMOD="$ROOT/experimental/ext/rooms/go.mod"

pinned() {
    awk '$1 == "github.com/panyam/mcpkit" && $2 ~ /^v/ { print substr($2, 2); exit }' "$GOMOD"
}
package_version() {
    sed -n 's/^  "version": "\(.*\)",$/\1/p' "$PKG" | head -1
}

case "${1:-}" in
check)
    want="$(pinned)"
    [ -n "$want" ] || { echo "no github.com/panyam/mcpkit require in $GOMOD" >&2; exit 1; }
    got="$(package_version)"
    bad=0
    if [ "$got" != "$want" ]; then
        echo "experimental/ext/rooms/ts/package.json is $got; experimental/ext/rooms/go.mod pins mcpkit v$want. make bump-root sets both." >&2
        bad=1
    fi
    if [ $# -gt 1 ] && [ "${2#v}" != "$want" ]; then
        echo "tag $2 doesn't match the rooms pin v$want" >&2
        bad=1
    fi
    [ $bad = 0 ] && echo "rooms-ts-version: @panyam/mcpkit-rooms at $want, matching the rooms pin"
    exit $bad
    ;;
set)
    v="${2:?usage: $0 set vX.Y.Z}"
    v="${v#v}"
    tmp="$(mktemp)"
    sed "s/^  \"version\": \".*\",\$/  \"version\": \"$v\",/" "$PKG" > "$tmp"
    if cmp -s "$tmp" "$PKG"; then
        rm -f "$tmp"
    else
        mv "$tmp" "$PKG"
        echo "==> experimental/ext/rooms/ts/package.json: version $v"
    fi
    ;;
*)
    echo "usage: $0 check [tag] | set vX.Y.Z" >&2
    exit 2
    ;;
esac
