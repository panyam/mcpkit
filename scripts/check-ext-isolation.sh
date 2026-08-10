#!/bin/sh
# Fail if one extension module directly requires another (constraint C4).
#
# Extensions are independent surfaces: each consumes core/ abstractions, not
# its siblings. Two things go wrong when that slips. A single import inverts
# the layering, so the module everyone else depends on now depends on one of
# them and version bumps entangle. And it implicitly stabilizes the imported
# module's API for the importer's benefit, though no SEP says the two must
# interoperate, so a later refactor of the callee breaks a caller nobody
# expected to exist.
#
# This used to be a bash block pasted inside CONSTRAINTS.md that nothing ran.
# A rule with an unrun verifier is a comment, and `agent/ext/` accumulated a
# second module while the block still only walked `ext/` and
# `experimental/ext/` (issue 1277).
#
# Membership is derived from the module path rather than a hardcoded list of
# directories: any module under a path segment named `ext` is an extension, so
# a new tree is covered the day it appears instead of the day someone
# remembers to add it here.
#
# Only DIRECT requires count, which is the part the old block got wrong:
#
#   - `replace` lines are path resolution for a multi-module repo. A replace
#     with no matching require changes no build and is not a dependency edge.
#   - `// indirect` requires are transitive. In this repo every agent/ext
#     module pulls ext/auth and friends through agent/host, and calling that a
#     C4 violation would flag 16 non-problems and train everyone to ignore the
#     check.
#
# The escape hatch for a genuine cross-extension test is unchanged: put it in
# a separate top-level module (tests/<a>_<b>_e2e/) that imports both.
#
# Run locally with: make check-ext-isolation

set -eu

cd "$(dirname "$0")/.."

# A module path is an extension when some segment after the repo root is
# literally `ext`: github.com/panyam/mcpkit[/<area>]/ext/<name>[/<sub>...]
ext_re='github\.com/panyam/mcpkit/([a-z0-9_-]+/)*ext/[a-zA-Z0-9_/-]+'

status=0

for gomod in $(find . -name go.mod -not -path '*/node_modules/*' | sort); do
  mod=$(awk '/^module /{print $2; exit}' "$gomod")

  # Not an extension module: nothing for C4 to say about what it requires.
  echo "$mod" | grep -qE "^$ext_re$" || continue

  # Direct requires only: inside a require block or on a require line, minus
  # anything marked indirect. Replace directives never reach this.
  refs=$(awk '
      /^require \(/ { inblock = 1; next }
      /^\)/         { inblock = 0 }
      inblock       { print; next }
      /^require /   { print }
    ' "$gomod" \
    | grep -v '// indirect' \
    | grep -oE "$ext_re" \
    | sort -u)

  for ref in $refs; do
    # Itself, or an ancestor of itself. A nested submodule depending on its
    # own parent (experimental/ext/events/stores/redis -> experimental/ext/events)
    # is intentional.
    case "$mod" in
      "$ref" | "$ref"/*) continue ;;
    esac
    echo "VIOLATION $gomod requires $ref"
    status=1
  done
done

if [ "$status" -ne 0 ]; then
  echo ""
  echo "C4: extension modules must not require each other. See CONSTRAINTS.md."
  echo "For a test that genuinely needs both, add tests/<ext-a>_<ext-b>_e2e/."
  exit 1
fi

echo "check-ext-isolation: no cross-extension requires."
