#!/usr/bin/env bash
# Secret scan stage of `make audit`. Fails on a finding. Skips with a note
# when gitleaks is not installed, since the other audit stages still apply.
#
# The previous inline recipe ran `gitleaks detect ... || echo "not installed"`,
# so a finding printed "not installed" and the audit passed. Known-fake values
# are allowlisted by exact value in .gitleaks.toml.
set -eu
cd "$(dirname "${BASH_SOURCE[0]}")/.."
if ! command -v gitleaks >/dev/null 2>&1; then
    echo "gitleaks not installed, skipping (go install github.com/gitleaks/gitleaks/v8@latest)"
    exit 0
fi
if ! gitleaks detect --source . --config .gitleaks.toml --redact -v; then
    echo "gitleaks: secrets found. If a finding is a known-fake test value, allowlist it by exact value in .gitleaks.toml." >&2
    exit 1
fi
