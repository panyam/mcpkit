#!/usr/bin/env bash
# Runs every host example in one of three modes.
#
# Usage: run-examples.sh <demo|note|readme>
#   demo    non-interactive walkthrough
#   note    notebook mode
#   readme  regenerate each example's README.md
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

EXAMPLES="${EXAMPLES:-}"
if [ -z "$EXAMPLES" ]; then
    echo "run-examples.sh: EXAMPLES is empty; the runner should pass it" >&2
    exit 2
fi

case "${1:?usage: run-examples.sh <demo|note|readme>}" in
    demo)   banner="Running %s";            flag="--non-interactive" ;;
    note)   banner="Running %s (notebook)"; flag="--note" ;;
    readme) banner="Generating %s/README.md" ;;
    *) echo "usage: run-examples.sh <demo|note|readme>" >&2; exit 2 ;;
esac

for ex in $EXAMPLES; do
    printf "=== $banner ===\n" "$ex"
    if [ "$1" = "readme" ]; then
        go run -buildvcs=false "./$ex/" --readme > "$ex/README.md"
    else
        go run -buildvcs=false "./$ex/" "$flag"
        echo
    fi
done
