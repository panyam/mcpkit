#!/usr/bin/env bash
# Rehearses a release end to end without publishing anything (#1488): clones
# this checkout, runs the real bump-pins and tag-all at a fake version, then
# runs check-install against the clone, served to `go` as if it were GitHub.
#
#   release-rehearsal.sh [vX.Y.Z]     # default v0.99.0
#
# It fails on anything that would make a tag uninstallable: a replace
# tag-all cannot strip, a command needing a sibling the bump missed, a module
# that only builds thanks to its replace. A real tag finds those only after it
# is public.
#
# The clone is reached through a git `insteadOf` rewrite of
# https://github.com/panyam/mcpkit. GOPRIVATE sends only that path and its
# sub-modules straight to git, and the module cache is a private one, so the
# fake version never reaches the proxy, the checksum database or the cache your
# builds use. Third-party modules still come from the usual proxy.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

V="${1:-v0.99.0}"
SRC="$PWD"
W="$(mktemp -d)"
trap 'chmod -R u+w "$W" 2>/dev/null; rm -rf "$W"' EXIT

# Committed work only: commit (or stash) before rehearsing a change.
git clone -q "$SRC" "$W/repo"
cd "$W/repo"
git config user.name rehearsal
git config user.email rehearsal@localhost

echo "==> bump-pins to $V"
V="$V" bash scripts/bump-pins.sh "$PWD" >/dev/null
git commit -q -am "release: bump to $V"

echo "==> tag-all at $V"
V="$V" bash scripts/tag-all.sh >/dev/null

if ! git diff --quiet HEAD~2 HEAD; then
    echo "release-rehearsal: the restore commit does not give back the pre-tag tree" >&2
    git diff --stat HEAD~2 HEAD >&2
    exit 1
fi

printf '[url "file://%s"]\n\tinsteadOf = https://github.com/panyam/mcpkit\n' "$W/repo" > "$W/gitconfig"
echo "==> check-install at $V (from the local clone)"
GIT_CONFIG_GLOBAL="$W/gitconfig" GIT_ALLOW_PROTOCOL=file:https \
    GOPRIVATE=github.com/panyam/mcpkit \
    GOMODCACHE="$W/modcache" \
    bash scripts/check-install.sh "$V"
