#!/usr/bin/env bash
# docs/site: build + force-push dist/docs to the gh-pages branch. Idempotent.
# Runner-agnostic; CI (.github/workflows/pages.yml) and manual deploys call this.
# GH_REMOTE_URL defaults to this repo's origin; override via env.
set -eu
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DIR"
: "${GH_REMOTE_URL:="$(git -C ../.. config --get remote.origin.url)"}"

# clean + build (the recipe's prerequisites, inlined so this is self-contained)
rm -rf dist
bash "$DIR/scripts/build.sh"

touch dist/docs/.nojekyll
if [ -d dist/docs/.git ]; then
    echo "Error: dist/docs already contains a .git directory."
    exit 1
fi
cd dist/docs && \
    git init -q && \
    git checkout -q -b gh-pages && \
    git add -A && \
    git -c user.name="mcpkit-pages" -c user.email="pages@mcpkit.local" \
        commit -q -m "Deploy mcpkit docs to GitHub Pages" && \
    git remote add origin "$GH_REMOTE_URL" && \
    git push -qf origin gh-pages
echo "Deployed dist/docs/ to gh-pages."
rm -rf dist/docs/.git
