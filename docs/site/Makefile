# docs/site/ — mcpkit GitHub Pages site (issue 508).
#
# Modeled on ~/projects/notations/docs/Makefile.  Source-of-truth markdown
# stays at the repository root and under docs/; this site only ships thin
# wrappers that render those files via goldmark.
#
# Targets:
#   make build        Build the site to dist/docs/.
#   make run          Start the dev server on :8085 with live rebuild.
#   make gh-pages     Build + force-push the result to the gh-pages branch.
#                     Used by .github/workflows/pages.yml; safe to run by
#                     hand once.

PORT ?= :8085
GH_REMOTE_URL ?= $(shell git -C ../.. config --get remote.origin.url)

.PHONY: build run gh-pages clean

build:
	@bash scripts/build.sh

run:
	MCPKIT_DOCS_ENV=dev MCPKIT_DOCS_PORT=$(PORT) go run .

clean:
	rm -rf dist

# Force-publish dist/docs to the gh-pages branch.  Idempotent: re-running
# overwrites the previously published snapshot.  CI invokes this from
# .github/workflows/pages.yml; the manual path is documented for one-shot
# deploys from a clean working tree. Body lives in scripts/gh-pages.sh
# (self-contained: it inlines the clean + build prerequisites).
gh-pages:
	@GH_REMOTE_URL="$(GH_REMOTE_URL)" bash scripts/gh-pages.sh
