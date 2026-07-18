# Attribution

The scenarios in this package are **hand-authored** by the mcpkit project, in
the spirit of and adapting the task categories from **LongMemEval**:

> LongMemEval: Benchmarking Chat Assistants on Long-Term Interactive Memory.
> https://github.com/xiaowu0162/LongMemEval

We borrow the *shape* of the benchmark — the five skill categories
(information extraction, multi-session reasoning, knowledge updates, temporal
reasoning, abstention) — and grade with our own `agent/eval` harness, which is
the differentiator. We do **not** vendor the LongMemEval dataset: the cases in
`cases.go` are original, short scenarios written to exercise the same skills
against mcpkit's working-memory tools.

`SmokeScenarios()` is deliberately a coarse regression signal, not a
published-comparable score. The rigorous bar is a follow-up **loader** that
reads the downloaded LongMemEval dataset from an env path and adapts each
question into an `eval.Scenario`, the same way the conformance suites point at
an external checkout via `MCPCONFORMANCE_*_PATH`. If you add that loader,
respect the upstream dataset's license and keep the data out of the default
test tree (env-pathed, downloaded on demand), exactly as the live benchmark
here requires an explicit endpoint to run.

The companion sibling benchmarks named in issue 974 — BFCL relevance
detection (exercises the approval gate via `NotDenied`) and tau-bench user
simulation (exercises the elicitation / MRTR input loop) — are separate
follow-ups and are not adapted here.
