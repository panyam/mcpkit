#!/usr/bin/env bash
# Exercises release-replaces.sh against a throwaway git repo: a root module, two
# tagged sub-modules carrying in-repo replaces (one single-line, one a block),
# a nested tagged module whose replace target is a bare `..`, and an untagged
# example that carries one too.
#   - strip drops every in-repo replace from the tagged modules
#   - strip leaves the untagged example's replace alone
#   - strip refuses a tagged module whose replace points outside the repo,
#     and edits nothing when it does
#   - restore after a strip commit gives back byte-identical go.mod files
#   - restore on any other HEAD changes nothing
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/release-replaces.sh"
T="$(mktemp -d)"
trap 'rm -rf "$T"' EXIT

fail=0
ok()  { echo "ok    $*"; }
bad() { echo "FAIL  $*"; fail=1; }
nrep() { (cd "$T/$1" && go mod edit -json) | python3 -c 'import json,sys;print(len(json.load(sys.stdin).get("Replace") or []))'; }

printf 'module github.com/panyam/mcpkit\n\ngo 1.26\n' > "$T/go.mod"
mkdir -p "$T/ext/a/c" "$T/ext/b" "$T/examples/x"
printf 'module github.com/panyam/mcpkit/ext/a\n\ngo 1.26\n\nrequire github.com/panyam/mcpkit v0.8.0\n\nreplace github.com/panyam/mcpkit => ../..\n' > "$T/ext/a/go.mod"
printf 'module github.com/panyam/mcpkit/ext/b\n\ngo 1.26\n\nrequire (\n\tgithub.com/panyam/mcpkit v0.8.0\n\tgithub.com/panyam/mcpkit/ext/a v0.8.0\n)\n\nreplace (\n\tgithub.com/panyam/mcpkit => ../..\n\tgithub.com/panyam/mcpkit/ext/a => ../a\n)\n' > "$T/ext/b/go.mod"
printf 'module github.com/panyam/mcpkit/ext/a/c\n\ngo 1.26\n\nrequire github.com/panyam/mcpkit/ext/a v0.8.0\n\nreplace github.com/panyam/mcpkit/ext/a => ..\n' > "$T/ext/a/c/go.mod"
printf 'module github.com/panyam/mcpkit/examples/x\n\ngo 1.26\n\nrequire github.com/panyam/mcpkit v0.8.0\n\nreplace github.com/panyam/mcpkit => ../..\n' > "$T/examples/x/go.mod"

git -C "$T" init -q
git -C "$T" -c user.name=t -c user.email=t@t add -A
git -C "$T" -c user.name=t -c user.email=t@t commit -qm base
cp "$T/ext/a/go.mod" "$T/a.before"; cp "$T/ext/b/go.mod" "$T/b.before"

restore_noop="$(bash "$SCRIPT" restore "$T")"
if git -C "$T" diff --quiet; then ok "restore on a non-strip HEAD changes nothing"; else bad "restore on a non-strip HEAD changed files"; fi
case "$restore_noop" in *"nothing to restore"*) ok "restore says so" ;; *) bad "restore printed: $restore_noop" ;; esac

SUB_MODS_TO_TAG="ext/a ext/b ext/a/c" bash "$SCRIPT" strip "$T"
[ "$(nrep ext/a)" = 0 ] && ok "ext/a single-line replace stripped" || bad "ext/a still has $(nrep ext/a) replace(s)"
[ "$(nrep ext/b)" = 0 ] && ok "ext/b replace block stripped" || bad "ext/b still has $(nrep ext/b) replace(s)"
[ "$(nrep ext/a/c)" = 0 ] && ok "ext/a/c bare .. replace stripped" || bad "ext/a/c still has $(nrep ext/a/c) replace(s)"
[ "$(nrep examples/x)" = 1 ] && ok "untagged examples/x keeps its replace" || bad "examples/x has $(nrep examples/x) replace(s), want 1"

git -C "$T" -c user.name=t -c user.email=t@t commit -qam "release: installable tree for v0.9.0"
bash "$SCRIPT" restore "$T" >/dev/null
if cmp -s "$T/ext/a/go.mod" "$T/a.before" && cmp -s "$T/ext/b/go.mod" "$T/b.before"; then
    ok "restore after the strip commit is byte-identical"
else
    bad "restore after the strip commit differs from the original"
fi

git -C "$T" checkout -q -- .
printf '\nreplace example.com/dep => example.com/fork v1.0.0\n' >> "$T/ext/a/go.mod"
if out="$(SUB_MODS_TO_TAG="ext/b ext/a" bash "$SCRIPT" strip "$T" 2>&1)"; then
    bad "strip accepted a replace that points outside the repo"
else
    case "$out" in *"example.com/dep"*) ok "strip refuses an out-of-repo replace and names it" ;; *) bad "strip refused without naming it: $out" ;; esac
    cmp -s "$T/ext/b/go.mod" "$T/b.before" && ok "a refused strip edits nothing" || bad "a refused strip still edited ext/b"
fi

[ "$fail" = 0 ] && echo "release-replaces-selftest: ok" || { echo "release-replaces-selftest: FAILED"; exit 1; }
