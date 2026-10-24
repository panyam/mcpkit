#!/usr/bin/env bash
# Release step: `go install` every command the tagged modules ship, at V, from
# an empty module, the way someone outside the repo would (#1488).
#
#   check-install.sh vX.Y.Z
#
# The command list is read from this checkout (every `package main` in the
# root and in SUB_MODS_TO_TAG, minus internal/ and testdata/), so run it from a
# tree that matches the tag. Every command is tried even after one fails, and
# the summary names them all.
#
# A fresh tag can take a few minutes to reach proxy.golang.org; a "not found"
# straight after tag-push usually means rerun, not broken.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

V="${1:-${V:-}}"
MODS="${SUB_MODS_TO_TAG:-}"
[ -n "$V" ] || { echo "usage: check-install.sh vX.Y.Z" >&2; exit 2; }

cmds=""
for dir in . $MODS; do
    found="$(cd "$dir" && go list -f '{{if eq .Name "main"}}{{.ImportPath}}{{end}}' ./... 2>/dev/null \
        | grep -v -e '/internal/' -e '/testdata/')"
    cmds="$cmds $found"
done
[ -n "${cmds// /}" ] || { echo "check-install: found no commands to install" >&2; exit 2; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/empty" "$work/bin"

failed=""
for pkg in $cmds; do
    if out="$(cd "$work/empty" && GOFLAGS= GOWORK=off GOBIN="$work/bin" go install "$pkg@$V" 2>&1)"; then
        echo "ok    $pkg@$V"
    else
        echo "FAIL  $pkg@$V"
        echo "$out" | sed 's/^/        /' | tail -6
        failed="$failed $pkg"
    fi
done

echo ""
if [ -n "$failed" ]; then
    echo "=== check-install FAILED at $V:"
    for pkg in $failed; do echo "  $pkg"; done
    exit 1
fi
echo "=== check-install: every command installs at $V ==="
