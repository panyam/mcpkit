#!/usr/bin/env bash
#
# Generate a mermaid graph of the walkthrough pages from their headers.
# Output goes to stdout; the Makefile redirects to GRAPH.md.
#
# Source of truth: each page's `> **Kind:** ... **Prerequisites:** [...]` header line.
# Edges run from prerequisite -> page (reading direction).
# Nodes are clickable -- mermaid renders them as links to the GitHub blob URL.
#
# Why absolute URLs: GitHub renders mermaid inside an iframe served from
# viewscreen.githubusercontent.com. Relative URLs ("./file.md") don't resolve
# against the repo from inside that sandbox -- they get mangled by GitHub's
# markdown viewer service. Absolute https://github.com/owner/repo/blob/...
# URLs work consistently. We auto-detect owner/repo from `git remote`.
#
# Overrides:
#   GRAPH_BLOB_BASE=<url>  full base URL (e.g. https://example.com/some/path/)
#   GRAPH_BRANCH=<branch>  branch name to use in the blob URL (default: main)

set -euo pipefail

cd "$(dirname "$0")"

META=("README.md" "STRUCTURE.md" "INDEX.md" "GRAPH.md")

# Detect blob URL base. e.g. https://github.com/panyam/mcpkit/blob/main/tutorials/walkthrough/
# Pulls owner/repo from the last two path segments of the git remote URL — works for
# direct github.com remotes, SSH remotes, and HTTP proxies that preserve the repo path.
# Assumes the destination is github.com; override with GRAPH_BLOB_BASE for other hosts.
detect_blob_base() {
    local remote owner repo branch prefix
    remote=$(git remote get-url origin 2>/dev/null) || return 1
    # Match last two path segments: .../<owner>/<repo>(.git)?(/)?
    if [[ "$remote" =~ ([^/:]+)/([^/.]+)(\.git)?/?$ ]]; then
        owner="${BASH_REMATCH[1]}"
        repo="${BASH_REMATCH[2]}"
        branch="${GRAPH_BRANCH:-main}"
        prefix=$(git rev-parse --show-prefix 2>/dev/null) || prefix=""
        echo "https://github.com/${owner}/${repo}/blob/${branch}/${prefix}"
        return 0
    fi
    return 1
}

blob_base="${GRAPH_BLOB_BASE:-}"
if [ -z "$blob_base" ]; then
    blob_base=$(detect_blob_base 2>/dev/null) || blob_base=""
fi

is_meta() {
    local f="$1"
    for m in "${META[@]}"; do
        [ "$f" = "$m" ] && return 0
    done
    return 1
}

# Extract the Kind value (e.g. "root" or "leaf"), stripping the FAQ-style suffix.
extract_kind() {
    local f="$1"
    grep -m1 -oE '\*\*Kind:\*\*[^·]+' "$f" 2>/dev/null \
        | sed 's/\*\*Kind:\*\* *//' \
        | sed 's/ *\*([^)]*)\*//' \
        | sed 's/ *$//' \
        || echo "page"
}

# Extract the basenames of prerequisite pages (without .md suffix).
extract_prereqs() {
    local f="$1"
    grep -m1 '\*\*Prerequisites:\*\*' "$f" 2>/dev/null \
        | grep -oE '\(\./[a-zA-Z-]+\.md\)' \
        | sed 's|(\./||;s|\.md)||' \
        || true
}

cat <<'HEADER'
# Walkthrough graph (auto-generated)

Run `make graph` to regenerate. Source of truth: each page's `**Prerequisites:**` header.
Nodes are clickable — they link to the page on GitHub (planned pages 404 by design).

```mermaid
graph TD
HEADER

# Emit nodes + click handlers (absolute URLs so GitHub's mermaid iframe can resolve them)
for f in *.md; do
    is_meta "$f" && continue
    base="${f%.md}"
    kind=$(extract_kind "$f")
    [ -z "$kind" ] && kind="page"
    printf '    %s["%s<br/>(%s)"]\n' "$base" "$base" "$kind"
    if [ -n "$blob_base" ]; then
        printf '    click %s "%s%s"\n' "$base" "$blob_base" "$f"
    else
        printf '    click %s "./%s"\n' "$base" "$f"
    fi
done

echo ""

# Emit edges from prerequisite -> page
for f in *.md; do
    is_meta "$f" && continue
    base="${f%.md}"
    extract_prereqs "$f" | while read -r p; do
        [ -z "$p" ] && continue
        printf '    %s --> %s\n' "$p" "$base"
    done
done

# Distinguish written vs planned visually.
echo ""
echo "    classDef written fill:#e8f5e9,stroke:#2e7d32,color:#000;"
written=""
for f in *.md; do
    is_meta "$f" && continue
    base="${f%.md}"
    if [ -z "$written" ]; then
        written="$base"
    else
        written="$written,$base"
    fi
done
[ -n "$written" ] && echo "    class $written written;"

echo '```'
