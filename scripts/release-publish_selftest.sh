#!/usr/bin/env bash
# Self-test for release-publish.sh with its three steps stubbed, so it runs in
# CI with no network: the GitHub Release is created only after both checks
# pass, and nothing runs for a stable tag with no release note.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT
LOG="$W/calls"

# stub NAME RC: a command that logs its name and arguments and exits RC.
stub() {
    printf '#!/usr/bin/env bash\necho "%s $*" >> "%s"\nexit %s\n' "$1" "$LOG" "$2" > "$W/$1"
    chmod +x "$W/$1"
}

failures=0
total=0
# check LABEL WANT_RC WANT_CALLS V WORKFLOWS_RC INSTALL_RC: WANT_CALLS is the
# space-separated list of steps expected to run, in order.
check() {
    local label=$1 want_rc=$2 want_calls=$3 v=$4
    total=$((total + 1))
    : > "$LOG"
    stub workflows "$5"
    stub install "$6"
    stub gh 0
    RELEASE_CHECK_WORKFLOWS="$W/workflows" RELEASE_CHECK_INSTALL="$W/install" GH="$W/gh" \
        bash scripts/release-publish.sh "$v" > "$W/out" 2>&1
    local rc=$?
    local calls
    calls="$(awk '{print $1}' "$LOG" | tr '\n' ' ' | sed 's/ $//')"
    if [ "$rc" != "$want_rc" ] || [ "$calls" != "$want_calls" ]; then
        failures=$((failures + 1))
        echo "selftest: $label"
        echo "  expected rc=$want_rc calls=[$want_calls], got rc=$rc calls=[$calls]"
        sed 's/^/    /' "$W/out"
    fi
}

# A stable tag needs docs/releases/<tag>.md. Use a real one so the selftest
# exercises the same path a release does.
stable=$(ls docs/releases/v*.md | grep -v -- '-b' | head -1 | xargs basename | sed 's/\.md$//')

check "all green: publishes"                      0 "workflows install gh" "$stable" 0 0
check "a workflow failed: no install, no release" 1 "workflows"            "$stable" 1 0
check "a command won't install: no release"       1 "workflows install"    "$stable" 0 1
check "stable tag without a note: nothing runs"   1 ""                     "v9.9.9" 0 0
check "pre-release without a note: publishes"     0 "workflows install gh" "v9.9.9-b1" 0 0

# The arguments gh gets matter too.
total=$((total + 1))
: > "$LOG"; stub workflows 0; stub install 0; stub gh 0
RELEASE_CHECK_WORKFLOWS="$W/workflows" RELEASE_CHECK_INSTALL="$W/install" GH="$W/gh" \
    bash scripts/release-publish.sh v9.9.9-b1 > /dev/null 2>&1
if ! grep -q -- "^gh release create v9.9.9-b1 .*--prerelease" "$LOG"; then
    failures=$((failures + 1))
    echo "selftest: a -bN tag is published with --prerelease"
    sed 's/^/    /' "$LOG"
fi
total=$((total + 1))
: > "$LOG"
RELEASE_CHECK_WORKFLOWS="$W/workflows" RELEASE_CHECK_INSTALL="$W/install" GH="$W/gh" \
    bash scripts/release-publish.sh "$stable" > /dev/null 2>&1
if ! grep -q -- "--notes-file docs/releases/$stable.md" "$LOG" || grep -q -- "--prerelease" "$LOG"; then
    failures=$((failures + 1))
    echo "selftest: a stable tag is published with its note and without --prerelease"
    sed 's/^/    /' "$LOG"
fi

echo "release-publish selftest: $((total - failures))/$total cases pass"
[ $failures -eq 0 ]
