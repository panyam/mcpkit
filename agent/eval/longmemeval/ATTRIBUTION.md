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

If you extend this package by importing LongMemEval data directly, respect the
upstream dataset's license and keep it out of the default test tree (behind
the `eval_llm` build tag, downloaded on demand), the same way the live
benchmark here requires an explicit endpoint to run.

The companion sibling benchmarks named in issue 974 — BFCL relevance
detection (exercises the approval gate via `NotDenied`) and tau-bench user
simulation (exercises the elicitation / MRTR input loop) — are separate
follow-ups and are not adapted here.
