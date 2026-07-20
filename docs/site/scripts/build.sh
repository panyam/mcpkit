#!/usr/bin/env bash
# docs/site: build the site to dist/docs/ (purges stale output first). Runner-agnostic.
set -eu
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DIR"
echo "Building mcpkit docs site..."
rm -rf dist
MCPKIT_DOCS_ENV=production go run . -build
cp -R static dist/docs/static
(cd ../.. && uv run scripts/collect_walkthroughs.py)
