#!/bin/bash
# Verifies that sub-module go.mod files pin their intra-repo dependencies at the
# version their release policy requires. Two policies, because this repo now has
# two release trains.
#
# Why any of this matters: `replace ../../` makes a v0.0.0 require work locally,
# but Go ignores replace directives in non-main modules. So the require line is
# the only thing a downstream `go get github.com/panyam/mcpkit/<mod>@vX` sees. A
# v0.0.0 require means the module does not resolve for anyone outside this repo.
#
#   PROTOCOL    Released and meant to be consumed. Every intra-repo require must
#               name a real tag, or the published module is broken. This is the
#               original check (#189).
#
#   AGENT       Lives under experimental/agent, deliberately unreleased (see
#               VERSIONING.md). Its agent-to-agent requires must STAY at v0.0.0,
#               because that is what makes the tree unresolvable from outside
#               while working fine in-repo. The inverted assertion is the point:
#               it stops someone "fixing" the pins and silently publishing the
#               agent surface. On extraction day, move these modules to the
#               protocol policy and run `make tag-agent`.
#
#   NON-LIBRARY cmd/*, tests/*, examples/*. Tagged for reproducibility but never
#               imported as libraries, so a v0.0.0 sibling pin harms nobody. The
#               root require is still checked.
#
# Failure mode the original version missed: it parsed ONLY the root
# github.com/panyam/mcpkit require and skipped every sibling require in the
# file. That is how agent/host pinned seven published modules at v0.0.0 and
# passed CI for months. Sibling requires are now in scope.

set -euo pipefail

# --resolve additionally checks that every version a published module names
# actually exists in the module proxy.
#
# Off by default because it needs the network, and CI should not fail on a
# proxy hiccup. It is the check that would have caught #1291 years earlier:
# the format check below only asks whether a version looks like a real tag,
# not whether that tag was ever pushed. Run it before a release; see
# RELEASING.md.
RESOLVE=0
if [ "${1:-}" = "--resolve" ]; then
    RESOLVE=1
fi

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

ROOT_MOD="github.com/panyam/mcpkit"
# Modules on the agent release line (unreleased on purpose).
AGENT_RE='^experimental/agent(/|$)'
# Modules that are tagged but never imported as a library.
NONLIB_RE='^(cmd|tests|examples|conformance|docs)/'

# The allowlist that used to sit here is gone: #1291 fixed the five placeholder
# pins it covered, so the protocol policy is now enforced with no exceptions.
# Reintroducing an exception should mean fixing the pin instead, because a
# published module that does not resolve is a worse break than one whose API
# moved.

# Discover every sub-module dynamically (mirrors the Makefile's SUB_MODS_ALL)
# so the check never goes stale when a sub-module is added or moved. For example,
# protogen relocating from ext/ to experimental/ext/ used to silently break
# this hardcoded list, and agent/ relocating to experimental/agent/ would have
# done the same. Modules that don't require the root are skipped below.
SUBMODULES=()
while IFS= read -r gomod; do
    SUBMODULES+=("$(dirname "${gomod#"$REPO_ROOT"/}")")
done < <(find "$REPO_ROOT" -name go.mod -not -path '*/node_modules/*' -not -path "$REPO_ROOT/go.mod" | sort)

# is_placeholder <version>. True only for versions that do not resolve.
#
# Two forms qualify: bare v0.0.0, and the zero pseudo-version
# v0.0.0-00010101000000-000000000000 that `go mod tidy` writes when a replace
# directive satisfies the requirement. Neither names a commit.
#
# A commit-based pseudo-version (v0.0.0-20260613221610-63a4e4058337) is NOT a
# placeholder, it resolves fine. Treating every v0.0.0-* as broken was the
# first cut of this function and it produced four false positives.
is_placeholder() {
    case "$1" in
        v0.0.0|v0.0.0-00010101000000-000000000000) return 0 ;;
        *) return 1 ;;
    esac
}

# direct_requires <gomod>. Every non-indirect intra-repo require, as
# "module version" pairs. Replace directives never reach this.
direct_requires() {
    awk '
        /^require \(/ { inblock = 1; next }
        /^\)/         { inblock = 0 }
        inblock       { print; next }
        /^require /   { sub(/^require /, ""); print }
    ' "$1" \
    | grep -v '// indirect' \
    | grep -oE "github\.com/panyam/mcpkit[a-zA-Z0-9_/.-]*[[:space:]]+v[^[:space:]]+" \
    | sed 's/[[:space:]]\+/ /g'
}

# resolve_check <module> <version>. Confirms the proxy serves that version,
# from a scratch module so this repo's replace directives cannot mask a break.
RESOLVE_DIR=""
resolve_check() {
    [ "$RESOLVE" = "1" ] || return 0
    if [ -z "$RESOLVE_DIR" ]; then
        RESOLVE_DIR="$(mktemp -d)"
        trap 'rm -rf "$RESOLVE_DIR"' EXIT
        (cd "$RESOLVE_DIR" && go mod init verifypins >/dev/null 2>&1)
    fi
    (cd "$RESOLVE_DIR" && go list -m "$1@$2" >/dev/null 2>&1)
}

fail=0
for sub in "${SUBMODULES[@]}"; do
    gomod="$REPO_ROOT/$sub/go.mod"
    if [ ! -f "$gomod" ]; then
        echo "MISSING: $gomod not found"
        fail=1
        continue
    fi

    if [[ "$sub" =~ $AGENT_RE ]]; then
        policy="agent"
    elif [[ "$sub" =~ $NONLIB_RE ]]; then
        policy="non-library"
    else
        policy="protocol"
    fi

    saw_root=0
    while read -r mod version; do
        [ -z "$mod" ] && continue

        if [ "$mod" = "$ROOT_MOD" ]; then
            # Every policy agrees on the root: it is published, so name a tag.
            saw_root=1
            if is_placeholder "$version"; then
                echo "FAIL: $sub/go.mod requires $ROOT_MOD $version (placeholder)"
                echo "      Bump to the current root tag. See RELEASING.md for the release order."
                fail=1
            fi
            continue
        fi

        case "$policy" in
            protocol)
                if is_placeholder "$version"; then
                    echo "FAIL: $sub/go.mod requires $mod $version (placeholder)"
                    echo "      A released module cannot pin a sibling at v0.0.0. It will not resolve"
                    echo "      for downstream consumers, because Go ignores replace outside the main module."
                    fail=1
                elif ! resolve_check "$mod" "$version"; then
                    echo "FAIL: $sub/go.mod requires $mod $version, which the proxy does not serve"
                    echo "      The version looks like a tag but no such tag was published."
                    fail=1
                fi
                ;;
            agent)
                # Only intra-agent requires carry the invariant. An agent module
                # pinning a *published* module (ext/otel, ext/auth) at a real tag
                # is harmless: that pin resolves, and unresolvability comes from
                # the agent-to-agent edges, which can never resolve because these
                # modules are never tagged. agentchat pins ext/otel v0.3.1 and is
                # still unreachable from outside via its ten agent-to-agent pins.
                case "$mod" in
                    "$ROOT_MOD"/experimental/agent|"$ROOT_MOD"/experimental/agent/*) ;;
                    *) continue ;;
                esac
                if ! is_placeholder "$version"; then
                    echo "FAIL: $sub/go.mod requires $mod $version (expected v0.0.0)"
                    echo "      Agent modules are unreleased on purpose, and their agent-to-agent pins"
                    echo "      are what keep them unresolvable from outside. See VERSIONING.md."
                    echo "      If you are extracting the tree, change AGENT_RE in this script instead."
                    fail=1
                fi
                ;;
            non-library)
                : # tagged but never imported as a library; sibling pins are harmless
                ;;
        esac
    done < <(direct_requires "$gomod")

    if [ "$saw_root" -eq 0 ]; then
        echo "PASS: $sub does not require $ROOT_MOD (skipping)"
    else
        echo "PASS: $sub ($policy policy)"
    fi
done

if [ $fail -ne 0 ]; then
    exit 1
fi

echo ""
echo "All sub-modules pin intra-repo deps per their release policy."
