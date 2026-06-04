#!/bin/bash
# Stage Anthropic's docx skill from the public anthropics/skills repo
# into examples/skills/skills/ so the demokit walkthrough can exercise a
# real-world multi-file skill alongside the bundled toy fixtures.
#
# The skill is fetched at run time rather than vendored to keep the
# example's git history small and to avoid licence ambiguity. Re-running
# is a no-op if the staged copy is up to date.
#
# Mirrors the fetch-at-build-time pattern used by
# scripts/apps-playwright-test.sh.
#
# Requires: git
set -euo pipefail

# Locate the example dir (this script lives at examples/skills/scripts/).
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
EXAMPLE_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
STAGE_DIR="$EXAMPLE_DIR/skills/docx"
CACHE_DIR="${ANTHROPIC_SKILLS_CACHE:-/tmp/anthropics-skills-cache}"
REPO_URL="https://github.com/anthropics/skills.git"

if ! command -v git >/dev/null 2>&1; then
    echo "fetch-docx: git not found on PATH" >&2
    exit 1
fi

if [ ! -d "$CACHE_DIR" ]; then
    echo "Cloning $REPO_URL into $CACHE_DIR..."
    git clone --depth 1 "$REPO_URL" "$CACHE_DIR"
else
    echo "Updating cached $CACHE_DIR..."
    git -C "$CACHE_DIR" fetch --depth 1 origin main
    git -C "$CACHE_DIR" reset --hard origin/main
fi

# Find the docx skill. The source repo layout has historically been
# skills/document-skills/docx, but allow either nesting depth.
SOURCE=""
for candidate in "$CACHE_DIR/skills/document-skills/docx" \
                 "$CACHE_DIR/skills/docx" \
                 "$CACHE_DIR/docx"; do
    if [ -f "$candidate/SKILL.md" ]; then
        SOURCE="$candidate"
        break
    fi
done

if [ -z "$SOURCE" ]; then
    echo "fetch-docx: could not find docx/SKILL.md under $CACHE_DIR" >&2
    echo "Inspect the upstream layout and tweak this script." >&2
    exit 1
fi

echo "Staging $SOURCE -> $STAGE_DIR"
rm -rf "$STAGE_DIR"
mkdir -p "$STAGE_DIR"
cp -R "$SOURCE/." "$STAGE_DIR/"

# SEP-2640 requires the frontmatter `name` to equal the final segment of
# the skill path. Our skill path will be "docx" (matching $STAGE_DIR's
# basename), so the upstream SKILL.md should already say `name: docx`.
# Verify and warn if not.
if ! grep -qE '^name:[[:space:]]*docx' "$STAGE_DIR/SKILL.md"; then
    echo "WARNING: $STAGE_DIR/SKILL.md frontmatter name does not equal 'docx'." >&2
    echo "         SEP-2640 requires the final skill-path segment to match the name." >&2
    echo "         The provider will reject this skill at startup. Edit the frontmatter or rename the dir." >&2
fi

echo "done. Restart 'make serve' to pick up the staged docx skill."
