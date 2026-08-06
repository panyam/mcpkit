#!/bin/sh
# Pre-commit hook for mcpkit.
#
# Rejects compiled executables and oversized files before they enter history.
#
# This exists because binaries kept landing in the repo by accident: a bare
# `go build` in a module directory drops an executable named after the
# directory (agent/surfaces/chat/chat, examples/.../server), which is easy to
# sweep in with `git add -A`. By the time anyone notices, the blob is in
# history and only a filter-repo rewrite removes it. One such cleanup took the
# repo from 466 MB to 23 MB.
#
# Detection is by content, not filename. Go build outputs have no extension, so
# an extension-based rule would miss every case that actually bit us.
#
# Bypass for a deliberate commit (a test fixture that must be a binary):
#   git commit --no-verify
#
# Installed by `just setup-hooks`. Edit this file in the repo, then
# `just setup-hooks` to reinstall. CI enforces the same rule for anyone who
# has not installed hooks — see .github/workflows/test.yml.

set -e
cd "$(git rev-parse --show-toplevel)"

# Files >5 MB are worth a second look even when not executable. Source files
# are never this large; this catches stray archives, dumps, and model weights.
MAX_BYTES=5242880

# Against an empty repo there is no HEAD to diff, so use the empty-tree hash.
if git rev-parse --verify HEAD >/dev/null 2>&1; then
    against=HEAD
else
    against=$(git hash-object -t tree /dev/null)
fi

bad_exec=""
bad_size=""

# --diff-filter=AM: only added/modified files. Deletions and renames of an
# already-tracked binary must not trip the hook, or removing one becomes
# impossible without --no-verify.
# -z + tr: NUL-delimited so paths with spaces survive.
staged=$(git diff --cached --name-only --diff-filter=AM -z "$against" | tr '\0' '\n')

[ -z "$staged" ] && exit 0

while IFS= read -r f; do
    [ -z "$f" ] && continue
    # Read from the index, not the worktree: the staged content is what would
    # be committed, and it can differ from what is on disk.
    blob=$(git rev-parse ":$f" 2>/dev/null) || continue

    # Magic bytes, read from the staged blob:
    #   7f454c46  ELF          (Linux)
    #   cffaedfe  Mach-O 64    (macOS, little-endian)
    #   cefaedfe  Mach-O 32    (little-endian)
    #   feedfacf  Mach-O 64    (big-endian, cross-compiled)
    #   feedface  Mach-O 32    (big-endian)
    #   cafebabe  Mach-O universal / Java class
    #   4d5a      MZ           (Windows PE)
    #   0061736d  wasm         (GOOS=js GOARCH=wasm)
    #   213c6172  !<arch>      (ar static archive, -buildmode=archive / cgo)
    #
    # The last four came from probing the check with one file per format: an
    # ELF/Mach-O-LE/PE-only list let big-endian Mach-O, wasm, and ar archives
    # through. wasm matters here because this repo has a web surface, so a
    # js/wasm build target is not hypothetical.
    magic=$(git cat-file blob "$blob" 2>/dev/null | od -An -tx1 -N4 | tr -d ' \n')
    case "$magic" in
        7f454c46|cffaedfe|cefaedfe|feedfacf|feedface|cafebabe|4d5a*|0061736d|213c6172)
            bad_exec="$bad_exec  $f\n"
            continue
            ;;
    esac

    size=$(git cat-file -s "$blob" 2>/dev/null || echo 0)
    if [ "$size" -gt "$MAX_BYTES" ]; then
        mb=$((size / 1048576))
        bad_size="$bad_size  $f (${mb} MB)\n"
    fi
done <<EOF
$staged
EOF

if [ -n "$bad_exec" ] || [ -n "$bad_size" ]; then
    echo "pre-commit: refusing to commit build artifacts." >&2
    echo >&2
    if [ -n "$bad_exec" ]; then
        echo "Compiled executables (detected by magic bytes):" >&2
        printf "$bad_exec" >&2
        echo >&2
        echo "These are almost always a stray 'go build' output. Unstage and ignore:" >&2
        echo "  git restore --staged <file>" >&2
        echo "  echo '/<name>' >> \$(dirname <file>)/.gitignore" >&2
        echo >&2
    fi
    if [ -n "$bad_size" ]; then
        echo "Files over 5 MB:" >&2
        printf "$bad_size" >&2
        echo >&2
    fi
    echo "If this file genuinely belongs in the repo: git commit --no-verify" >&2
    exit 1
fi

exit 0
