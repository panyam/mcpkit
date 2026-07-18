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
	@echo "Building mcpkit docs site..."
	@rm -rf dist   # purge stale output so removed pages never linger in a deploy
	MCPKIT_DOCS_ENV=production go run . -build
	@cp -R static dist/docs/static
	@cd ../.. && uv run scripts/collect_walkthroughs.py

run:
	MCPKIT_DOCS_ENV=dev MCPKIT_DOCS_PORT=$(PORT) go run .

clean:
	rm -rf dist

# Force-publish dist/docs to the gh-pages branch.  Idempotent: re-running
# overwrites the previously published snapshot.  CI invokes this from
# .github/workflows/pages.yml; the manual path is documented for one-shot
# deploys from a clean working tree.
gh-pages: clean build
	@touch dist/docs/.nojekyll
	@if [ -d dist/docs/.git ]; then \
		echo "Error: dist/docs already contains a .git directory."; \
		exit 1; \
	fi
	@cd dist/docs && \
		git init -q && \
		git checkout -q -b gh-pages && \
		git add -A && \
		git -c user.name="mcpkit-pages" -c user.email="pages@mcpkit.local" \
			commit -q -m "Deploy mcpkit docs to GitHub Pages" && \
		git remote add origin "$(GH_REMOTE_URL)" && \
		git push -qf origin gh-pages
	@echo "Deployed dist/docs/ to gh-pages."
	@rm -rf dist/docs/.git
