import { createSignal, type Accessor } from "solid-js";
import { HostEventKind, type HostEvent } from "./hostevent.js";

// budget.ts projects per-turn provider usage into running gauges: cumulative
// input/output tokens, total model steps, turn count, and the last turn's
// breakdown. It reduces the HostTurnDone stream (each carries the turn's
// TurnResult with Usage + Steps). Tree-budget caps are configuration, not
// events, so this shows consumption; a max would come from a future usage
// event. Pure reduce, unit-testable.

export interface BudgetState {
  turns: number;
  inputTokens: number;
  outputTokens: number;
  steps: number;
  lastInput: number;
  lastOutput: number;
  lastSteps: number;
}

const ZERO: BudgetState = {
  turns: 0,
  inputTokens: 0,
  outputTokens: 0,
  steps: 0,
  lastInput: 0,
  lastOutput: 0,
  lastSteps: 0,
};

export interface BudgetStore {
  state: Accessor<BudgetState>;
  ingest: (ev: HostEvent) => void;
  reset: () => void;
}

// fold applies one finished turn's result to the running state. Exported for the
// test so the accumulation is checked without a store.
export function fold(prev: BudgetState, ev: HostEvent): BudgetState {
  if (ev.Kind !== HostEventKind.TurnDone) return prev;
  const u = ev.Result?.usage;
  const input = u?.inputTokens ?? 0;
  const output = u?.outputTokens ?? 0;
  const steps = ev.Result?.steps ?? 0;
  return {
    turns: prev.turns + 1,
    inputTokens: prev.inputTokens + input,
    outputTokens: prev.outputTokens + output,
    steps: prev.steps + steps,
    lastInput: input,
    lastOutput: output,
    lastSteps: steps,
  };
}

export function createBudgetStore(): BudgetStore {
  const [state, setState] = createSignal<BudgetState>({ ...ZERO });
  return {
    state,
    ingest: (ev) => setState((prev) => fold(prev, ev)),
    reset: () => setState({ ...ZERO }),
  };
}
