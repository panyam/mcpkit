#!/usr/bin/env bash
# any-frontend: typecheck web/ and fail if the committed views/*.html are
# stale against their sources. Go embeds the committed files, so a source
# edit without a rebuild ships the old View.
set -eu
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DIR/web"
pnpm install --frozen-lockfile --silent
pnpm check
built="upstream-vanilla.html upstream-react.html upstream-extras.html host.html"
snap="$(mktemp -d)"
trap 'rm -rf "$snap"' EXIT
for f in $built; do cp "../views/$f" "$snap/$f"; done
pnpm build >/dev/null
stale=0
for f in $built; do
  if ! cmp -s "$snap/$f" "../views/$f"; then
    echo "views/$f was stale against web/. It has been rebuilt: commit it." >&2
    stale=1
  fi
done
exit "$stale"
