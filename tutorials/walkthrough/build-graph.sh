#!/usr/bin/env bash
#
# Generate a mermaid graph of the walkthrough pages from their headers.
# Output goes to stdout; the Makefile redirects to GRAPH.md.
#
# Source of truth: each page's `> **Kind:** ... **Prerequisites:** [...]` header line.
# Edges run from prerequisite -> page (reading direction).
# Nodes are clickable -- mermaid renders them as links to ./<filename>.md.

set -euo pipefail

cd "$(dirname "$0")"

META=("README.md" "STRUCTURE.md" "INDEX.md" "GRAPH.md")

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
Nodes are clickable — they link to the page on disk (or 404 if planned-but-unwritten).

```mermaid
graph TD
HEADER

# Emit nodes + click handlers
for f in *.md; do
    is_meta "$f" && continue
    base="${f%.md}"
    kind=$(extract_kind "$f")
    [ -z "$kind" ] && kind="page"
    printf '    %s["%s<br/>(%s)"]\n' "$base" "$base" "$kind"
    printf '    click %s "./%s"\n' "$base" "$f"
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
