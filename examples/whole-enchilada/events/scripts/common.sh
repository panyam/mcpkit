#!/usr/bin/env bash
# Shared helpers for this example's recipe scripts. Sourced, never executed.
#
# RUNNER is how a usage message names what the operator actually typed. The two
# runners take their arguments differently — `make poller TENANT=A` versus
# `TENANT=A just poller` — and both messages predate this script, so both are
# reproduced rather than unified.
RUNNER="${RUNNER:-make}"

# tenant_realm maps a shorthand TENANT letter to the canonical realm name.
# A -> asgard, B -> babylon, C -> camelot. A literal realm name (or any custom
# realm) is preserved unchanged.
#
# This existed twice before C8: once as Make's nested `$(filter ...)` chain and
# once as just's `if TENANT =~ '(?i)^a$'` chain. Two implementations of one
# three-case mapping, in two languages, is the whole argument for the rule.
tenant_realm() {
    case "${1:-}" in
        A|a) echo asgard ;;
        B|b) echo babylon ;;
        C|c) echo camelot ;;
        *)   echo "${1:-}" ;;
    esac
}

# usage_for prints the invocation in the dialect of whichever runner called us,
# then exits 2 — the code both recipes already used.
usage_for() {
    local recipe="$1"
    shift
    if [ "$RUNNER" = "just" ]; then
        echo "usage: $* just $recipe" >&2
    else
        echo "usage: make $recipe $*" >&2
    fi
    exit 2
}

# The dev-stack compose invocation. Both runners build this same string from
# STACK + DEV_OVERLAY; a script that needs to reach into a container calls this
# instead of receiving a pre-joined command line it would have to word-split.
STACK="${STACK:-events-stack.yaml}"
DEV_OVERLAY="${DEV_OVERLAY:-compose.dev.yaml}"

compose() {
    docker compose -f "$STACK" -f "$DEV_OVERLAY" "$@"
}
