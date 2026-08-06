import { describe, expect, it } from "vitest";
import { createRoot } from "solid-js";
import { createTimelineStore, summarize, MAX_ENTRIES } from "./timeline.js";
import type { HostEvent } from "./hostevent.js";

function run(events: HostEvent[], filter?: string) {
  return createRoot((dispose) => {
    const store = createTimelineStore();
    for (const ev of events) store.ingest(ev);
    if (filter !== undefined) store.setFilter(filter);
    const out = { entries: store.entries(), kinds: store.kinds() };
    dispose();
    return out;
  });
}

describe("timeline summarize", () => {
  it("unwraps a runner event to its inner agent.Event kind", () => {
    expect(summarize({ Kind: "runner-event", RunnerEvent: { kind: "tool-begin", toolCall: { id: "1", name: "search" } } })).toEqual({
      kind: "tool-begin",
      summary: "→ search",
    });
    expect(summarize({ Kind: "turn-done", Result: { steps: 3 } })).toEqual({ kind: "turn-done", summary: "turn done (3 steps)" });
    expect(summarize({ Kind: "handoff", From: "triage", To: "billing" })).toEqual({ kind: "handoff", summary: "triage → billing" });
  });
});

describe("TimelineStore reduce", () => {
  it("records every event in order and collects the distinct kinds seen", () => {
    const out = run([
      { Kind: "session-changed", RunID: "r1" },
      { Kind: "runner-event", RunnerEvent: { kind: "text-delta", text: "hi" } },
      { Kind: "runner-event", RunnerEvent: { kind: "tool-begin", toolCall: { id: "1", name: "search" } } },
      { Kind: "turn-done", Result: { steps: 1 } },
    ]);
    expect(out.entries.map((e) => e.kind)).toEqual(["session-changed", "text-delta", "tool-begin", "turn-done"]);
    // kinds is sorted and deduped for the filter dropdown.
    expect(out.kinds).toEqual(["session-changed", "text-delta", "tool-begin", "turn-done"]);
  });

  it("applies a kind filter to the projected entries", () => {
    const out = run(
      [
        { Kind: "runner-event", RunnerEvent: { kind: "tool-begin", toolCall: { id: "1", name: "a" } } },
        { Kind: "runner-event", RunnerEvent: { kind: "text-delta", text: "x" } },
        { Kind: "runner-event", RunnerEvent: { kind: "tool-begin", toolCall: { id: "2", name: "b" } } },
      ],
      "tool-begin",
    );
    expect(out.entries.map((e) => e.summary)).toEqual(["→ a", "→ b"]);
  });

  it("bounds the ledger to the most recent MAX_ENTRIES", () => {
    const many: HostEvent[] = Array.from({ length: MAX_ENTRIES + 50 }, (_, i) => ({
      Kind: "message",
      Message: `m${i}`,
    }));
    const out = run(many);
    expect(out.entries).toHaveLength(MAX_ENTRIES);
    // The oldest 50 were dropped; the newest is last.
    expect(out.entries[out.entries.length - 1].summary).toBe(`m${MAX_ENTRIES + 49}`);
  });
});
