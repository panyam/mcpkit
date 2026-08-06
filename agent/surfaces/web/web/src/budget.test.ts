import { describe, expect, it } from "vitest";
import { fold } from "./budget.js";
import type { HostEvent } from "./hostevent.js";

const turn = (input: number, output: number, steps: number): HostEvent => ({
  Kind: "turn-done",
  Result: { usage: { inputTokens: input, outputTokens: output }, steps },
});

describe("budget fold", () => {
  it("accumulates usage across turns and tracks the last turn", () => {
    let s = fold(fold(undefinedZero(), turn(100, 20, 2)), turn(50, 10, 1));
    expect(s).toMatchObject({
      turns: 2,
      inputTokens: 150,
      outputTokens: 30,
      steps: 3,
      lastInput: 50,
      lastOutput: 10,
      lastSteps: 1,
    });
  });

  it("ignores non-turn-done events and missing usage", () => {
    const zero = undefinedZero();
    expect(fold(zero, { Kind: "message", Message: "hi" })).toBe(zero);
    // A turn-done with no usage still counts the turn, adds zero tokens.
    expect(fold(zero, { Kind: "turn-done", Result: { steps: 1 } })).toMatchObject({ turns: 1, inputTokens: 0, steps: 1 });
  });
});

function undefinedZero() {
  return { turns: 0, inputTokens: 0, outputTokens: 0, steps: 0, lastInput: 0, lastOutput: 0, lastSteps: 0 };
}
