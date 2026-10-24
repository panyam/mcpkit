#!/usr/bin/env bash
# Strips the in-repo `replace` directives from every tagged module, or puts
# them back, so a release tag points at a tree `go install pkg@V` accepts (#1488).
#
#   release-replaces.sh strip [ROOT]     # tag-all.sh, just before tagging
#   release-replaces.sh restore [ROOT]   # tag-all.sh just after; vulncheck.yml on a tag
#
# Main keeps the replaces so every module builds against the tree it sits in.
# Go ignores them when a module is fetched as a dependency, but `go install
# pkg@version` refuses any module that carries one, which is why no command
# here had installed at any tag. tag-all.sh commits the stripped tree, tags
# that commit, and commits the restore straight after, so the tags stay
# ancestors of main and main never builds without its replaces.
#
# restore works from git rather than from memory: if HEAD is the strip commit
# (its subject starts with STRIP_SUBJECT), it checks out HEAD^'s copy of every
# go.mod HEAD changed. On any other HEAD it does nothing, so a scheduled
# vulncheck run on main can call it unconditionally.
set -euo pipefail

STRIP_SUBJECT="release: installable tree for "

cmd="${1:-}"
ROOT="$(cd "${2:-$(dirname "${BASH_SOURCE[0]}")/..}" && pwd)"
MODS="${SUB_MODS_TO_TAG:-}"

die() { echo "release-replaces: $*" >&2; exit 1; }

# Prints one "old<TAB>new" line per replace directive in the go.mod under $1.
replaces() {
    (cd "$1" && go mod edit -json) | python3 -c '
import json, sys
for r in json.load(sys.stdin).get("Replace") or []:
    print(r["Old"]["Path"] + "\t" + r["New"]["Path"])'
}

strip() {
    [ -n "$MODS" ] || die "SUB_MODS_TO_TAG is empty"
    local mod old new drops="" bad=""
    for mod in $MODS; do
        [ -f "$ROOT/$mod/go.mod" ] || die "$mod: no go.mod"
        while IFS=$'\t' read -r old new; do
            [ -n "$old" ] || continue
            case "$old" in
                github.com/panyam/mcpkit|github.com/panyam/mcpkit/*)
                    case "$new" in
                        .|..|./*|../*) drops="$drops $mod=$old"; continue ;;
                    esac ;;
            esac
            bad="$bad
  $mod: replace $old => $new"
        done < <(replaces "$ROOT/$mod")
    done
    # Any replace left makes that module uninstallable at the tag, and only an
    # in-repo one is ours to drop. Checked before editing anything, so a refusal
    # leaves the tree as it was.
    [ -z "$bad" ] || die "replaces that are not in-repo, so the tag would still refuse go install:$bad"
    local d
    for d in $drops; do
        (cd "$ROOT/${d%%=*}" && go mod edit -dropreplace="${d#*=}")
    done
}

restore() {
    local subject
    subject="$(git -C "$ROOT" log -1 --format=%s)"
    case "$subject" in
        "$STRIP_SUBJECT"*) ;;
        *) echo "release-replaces: HEAD is not a strip commit, nothing to restore"; return 0 ;;
    esac
    git -C "$ROOT" rev-parse -q --verify HEAD^ >/dev/null \
        || die "HEAD is a strip commit but HEAD^ is missing (shallow clone? fetch-depth: 2)"
    local files
    files="$(git -C "$ROOT" diff --name-only HEAD^ HEAD -- '*go.mod')"
    [ -n "$files" ] || return 0
    # shellcheck disable=SC2086
    (cd "$ROOT" && git checkout HEAD^ -- $files)
}

case "$cmd" in
    strip) strip ;;
    restore) restore ;;
    *) die "usage: release-replaces.sh strip|restore [ROOT]" ;;
esac
