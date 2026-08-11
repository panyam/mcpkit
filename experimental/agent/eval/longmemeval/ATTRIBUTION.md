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
published-comparable score.

## The dataset loader

`DatasetAdapter` reads the **actual** released corpus and adapts each question
into an `eval.Scenario`. The data is **not vendored**: point
`LONGMEMEVAL_DATA_PATH` at a downloaded file, the same way the conformance
suites point at an external checkout via `MCPCONFORMANCE_*_PATH`.

The corpus is released under the **MIT license** at
`huggingface.co/datasets/xiaowu0162/longmemeval-cleaned` as
`longmemeval_s.json`, `longmemeval_m.json`, and `longmemeval_oracle.json`,
500 instances each.

```bash
export LONGMEMEVAL_DATA_PATH=/path/to/longmemeval_s.json
export LONGMEMEVAL_LIMIT=20          # a full pass is 500 x ~115k tokens
go test -tags eval_llm ./experimental/agent/eval/longmemeval/ -run TestLive -v
```

**Two modes, measuring different systems, not comparable to each other.**
The default puts each haystack session into a `MemoryStore` and grades whether
the agent retrieves the right one, which is the stack this repo ships and the
only mode a small-context model can compete in. `LongContext: true` seeds every
session into the conversation instead, which is what published baselines mostly
report and what fails rather than scores when a haystack exceeds the model's
window.

Numbers from either mode are ours, not the benchmark authors'. Any comparison
to published results should say which mode produced it.

The companion sibling benchmarks named in issue 974 — BFCL relevance
detection (exercises the approval gate via `NotDenied`) and tau-bench user
simulation (exercises the elicitation / MRTR input loop) — are separate
follow-ups and are not adapted here.
