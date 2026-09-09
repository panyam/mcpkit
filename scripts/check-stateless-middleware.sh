#!/usr/bin/env bash
# C6 gate — every stateless dispatcher handler for a request-bearing method
# must route through Backend.InvokeWithMiddleware, so that a middleware
# applies identically on the session and stateless wires.
#
# The session wire wraps d.Dispatch with the middleware chain and filters by
# nothing (server/server.go), so it sees every method. The stateless wire
# dispatches per-method in server/stateless/handlers.go, and a handler that
# does not call InvokeWithMiddleware silently skips the chain. For an
# authorization middleware that is a bypass, which is what happened to
# resources/read (fixed in #1352).
#
# ALLOWED lists the handlers knowingly not routed today. Shrinking it is the
# point; adding to it needs a reason in the PR.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.." || exit 2
FILE=server/stateless/handlers.go

# Enumeration and completion are not routed yet; see issue in ALLOWED_REASON.
ALLOWED="handleToolsList handleResourcesList handleResourcesTemplatesList handlePromptsList handleCompletionComplete"

fail=0
for h in $(grep -o "func (d \*Dispatcher) handle[A-Za-z]*" "$FILE" | sed 's/.*) //'); do
    body=$(sed -n "/func (d \*Dispatcher) ${h}(/,/^}/p" "$FILE")
    if echo "$body" | grep -q "InvokeWithMiddleware"; then
        continue
    fi
    case " $ALLOWED " in
        *" $h "*) continue ;;
    esac
    echo "check-stateless-middleware: ${h} does not route through InvokeWithMiddleware."
    echo "  A middleware applied to this method works on the session wire and not on this one."
    echo "  Route it, or add it to ALLOWED in $0 with a reason."
    fail=1
done

if [ "$fail" -eq 0 ]; then
    echo "check-stateless-middleware: ok ($(echo $ALLOWED | wc -w | tr -d ' ') handler(s) knowingly unrouted)"
fi
exit $fail
