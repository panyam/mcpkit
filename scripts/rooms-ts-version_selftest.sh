#!/usr/bin/env bash
# Self-test for scripts/rooms-ts-version.sh against a scratch tree, so it never
# touches the real package.json.
set -u
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/rooms-ts-version.sh"
TMP=$(mktemp -d -t rooms-ts-version-selftest.XXXXXX)
mkdir -p "$TMP/scripts" "$TMP/experimental/ext/rooms/ts"
cp "$SRC" "$TMP/scripts/"
printf 'module x\n\nrequire (\n\tgithub.com/panyam/mcpkit v1.2.3\n)\n' > "$TMP/experimental/ext/rooms/go.mod"
printf '{\n  "name": "@panyam/mcpkit-rooms",\n  "version": "1.2.3",\n  "type": "module"\n}\n' > "$TMP/experimental/ext/rooms/ts/package.json"
S="$TMP/scripts/rooms-ts-version.sh"

FAILS=0
check() {
    if [ "$2" = "$3" ]; then echo "  ok    $1"; else echo "  FAIL  $1: want $2, got $3"; FAILS=$((FAILS + 1)); fi
}

bash "$S" check >/dev/null 2>&1
check "a package at the pinned version passes" 0 $?
bash "$S" check v1.2.3 >/dev/null 2>&1
check "the matching tag passes" 0 $?
bash "$S" check v1.2.4 >/dev/null 2>&1
check "another tag fails" 1 $?
bash "$S" set v2.0.0 >/dev/null
bash "$S" check >/dev/null 2>&1
check "a package ahead of the pin fails" 1 $?
grep -q '"version": "2.0.0",' "$TMP/experimental/ext/rooms/ts/package.json"
check "set rewrites only the version" 0 $?
grep -q '"name": "@panyam/mcpkit-rooms",' "$TMP/experimental/ext/rooms/ts/package.json"
check "set leaves the other fields" 0 $?

rm -rf "$TMP"
if [ "$FAILS" -ne 0 ]; then echo "rooms-ts-version selftest: $FAILS case(s) failed"; exit 1; fi
echo "rooms-ts-version selftest: 6/6 cases pass"
