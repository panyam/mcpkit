# Security Policy

## Reporting a vulnerability

Report vulnerabilities privately through [GitHub private vulnerability reporting](https://github.com/panyam/mcpkit/security/advisories/new) on this repository. If that is unavailable, email sri.panyam@gmail.com with "mcpkit security" in the subject.

Do not open a public issue for a suspected vulnerability.

## Response expectations

- Acknowledgement and triage within 2 business days.
- Critical issues (CVSS 7.0 or higher, or anything that breaks core MCP operations such as connection establishment, message exchange, or the tools/resources/prompts primitives) are fixed or mitigated within 7 days, tracked under the `P0` label once disclosed.
- A fix ships as a patch release on the current release line, with the advisory published after the release is available.

## Supported versions

The latest tagged minor release line receives security fixes. Older lines are fixed only when a patch backports cleanly.

## Scope

The root module and all sub-modules in this repository (`agent/`, `ext/auth/`, `ext/tasks/`, `ext/ui/`, `ext/otel/`, `experimental/...`). Continuous checks run via `make audit` (govulncheck, gosec, gitleaks, race detector) and in CI.
