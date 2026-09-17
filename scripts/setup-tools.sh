#!/usr/bin/env bash
# The audit toolchain: govulncheck, gosec, staticcheck, gitleaks.
set -euo pipefail

TOOLS="
golang.org/x/vuln/cmd/govulncheck@latest
github.com/securego/gosec/v2/cmd/gosec@latest
honnef.co/go/tools/cmd/staticcheck@latest
github.com/gitleaks/gitleaks/v8@latest
"

for tool in $TOOLS; do
    go install "$tool"
done
