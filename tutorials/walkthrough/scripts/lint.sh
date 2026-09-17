#!/usr/bin/env bash
# Link and stub bookkeeping for the walkthrough pages.
#
# Usage: lint.sh <check|missing|stats>
#   missing  list link targets that do not exist
#   check    the gate: broken links that INDEX.md does not list as forthcoming
#   stats    page counts by kind, and a broken-link tally
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

META_RE='(README|STRUCTURE|INDEX|GRAPH)\.md'
LINK_RE='\(\./[a-zA-Z-]+\.md\)'

# Content pages: every *.md that is not one of the meta pages.
pages() {
    ls ./*.md 2>/dev/null | sed 's|^\./||' | grep -vE "^$META_RE" || true
}

# Link targets referenced by a file (or by every *.md when given none).
link_targets() {
    grep -hoE "$LINK_RE" "$@" 2>/dev/null | sed 's|(\./||;s|)||' | sort -u
}

cmd_missing() {
    local link
    for link in $(link_targets ./*.md); do
        [ -f "$link" ] || echo "$link"
    done
}

cmd_check() {
    local planned bad=0 f target
    planned=$(awk '/^## Forthcoming nodes/{flag=1; next} /^## /{flag=0} flag' INDEX.md \
        | grep -oE "$LINK_RE" | sed 's|(\./||;s|)||' | sort -u)

    for f in $(pages) README.md STRUCTURE.md INDEX.md; do
        [ -f "$f" ] || continue
        for target in $(link_targets "$f"); do
            [ -f "$target" ] && continue
            echo "$planned" | grep -qx "$target" && continue
            printf "BROKEN: %s -> %s  (404 and not listed in INDEX forthcoming-nodes)\n" "$f" "$target"
            bad=1
        done
    done

    [ "$bad" = "0" ] && echo "ok: all internal links resolve to written or canonically-planned pages."
    return "$bad"
}

cmd_stats() {
    local pages count
    pages=$(pages)
    count() { echo "$1" | wc -l | tr -d ' '; }

    printf "Content pages:    %s\n" "$(echo $pages | wc -w | tr -d ' ')"
    printf "  roots:          %s\n" "$(grep -l '\*\*Kind:\*\* root' $pages 2>/dev/null | wc -l | tr -d ' ')"
    printf "  branches:       %s\n" "$(grep -l '\*\*Kind:\*\* branch' $pages 2>/dev/null | wc -l | tr -d ' ')"
    printf "  leaves:         %s\n" "$(grep -l '\*\*Kind:\*\* leaf' $pages 2>/dev/null | wc -l | tr -d ' ')"
    printf "  written:        %s\n" "$( { for f in $pages; do grep -L '^<!-- STUB -->' "$f" 2>/dev/null || true; done; } | wc -l | tr -d ' ')"
    printf "  stubs:          %s\n" "$(grep -l '^<!-- STUB -->' $pages 2>/dev/null | wc -l | tr -d ' ')"
    printf "Stub-link refs:   %s unique\n" "$(grep -hoE '\*\(stub[^)]*\)\*' ./*.md 2>/dev/null | wc -l | tr -d ' ')"
    printf "Broken links:     %s file(s)\n" "$(cmd_missing | wc -l | tr -d ' ')"
}

case "${1:?usage: lint.sh <check|missing|stats>}" in
    missing) cmd_missing ;;
    check)   cmd_check ;;
    stats)   cmd_stats ;;
    *) echo "usage: lint.sh <check|missing|stats>" >&2; exit 2 ;;
esac
