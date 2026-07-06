---
name: commit-helper
description: Turn a change description into a Conventional Commits subject line using the format_commit tool
---

# Commit Helper

When the user describes a change they made, produce a Conventional Commits
subject line for it.

Call the `format_commit` tool with:

- `type` — one of `feat`, `fix`, `docs`, `refactor`, `test`, `chore`
- `scope` — optional; the package or area that changed
- `summary` — a short, imperative one-line description

Return the tool's output verbatim as the commit subject. Do not hand-format
the subject yourself; the tool owns the formatting rules.
