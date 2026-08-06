#!/bin/sh
# Fail if any tracked file is a compiled executable.
#
# The pre-commit hook is the fast local signal, but it is opt-in (installed by
# `just setup-hooks`) and bypassable with --no-verify. This is the enforcing
# check: it runs in CI over the whole tracked tree, so a binary cannot land on
# main regardless of anyone's local setup.
#
# Whole-tree rather than diff-only: scanning the diff would let a binary that
# slipped in earlier stay forever, and the tree scan is fast enough (~1s).
#
# Run locally with: just check-no-binaries

set -e
cd "$(git rev-parse --show-toplevel)"

found=""

# -z + tr so paths with spaces survive.
for f in $(git ls-files -z | tr '\0' '\n'); do
    [ -f "$f" ] || continue
    # ELF / Mach-O (both byte orders) / universal / PE / wasm / ar archive.
    # Kept in sync with scripts/pre-commit-hook.sh, which documents each byte
    # sequence and why it is listed.
    magic=$(od -An -tx1 -N4 "$f" 2>/dev/null | tr -d ' \n')
    case "$magic" in
        7f454c46|cffaedfe|cefaedfe|feedfacf|feedface|cafebabe|4d5a*|0061736d|213c6172)
            found="$found  $f\n"
            ;;
    esac
done

if [ -n "$found" ]; then
    echo "check-no-binaries: compiled executables are tracked in the repo:" >&2
    printf "$found" >&2
    echo >&2
    echo "Remove from tracking (keeps the local file) and ignore it:" >&2
    echo "  git rm --cached <file>" >&2
    echo "  echo '/<name>' >> \$(dirname <file>)/.gitignore" >&2
    exit 1
fi

echo "check-no-binaries: no compiled executables tracked."
