import { describe, expect, it } from "vitest";
import { createRoot } from "solid-js";
import { createSubAgentStore } from "./subagents.js";
import type { AgentEvent, HostEvent } from "./hostevent.js";

// sa builds a HostSubAgentEvent frame for scope/depth with an inner agent.Event.
function sa(scope: string, depth: number, inner: AgentEvent): HostEvent {
  return { Kind: "sub-agent-event", SubAgent: { Scope: scope, Depth: depth, Event: inner } };
}

function run(events: HostEvent[]) {
  return createRoot((dispose) => {
    const store = createSubAgentStore();
    for (const ev of events) store.ingest(ev);
    const tree = store.tree();
    dispose();
    return tree;
  });
}

describe("SubAgentStore tree assembly", () => {
  it("nests a child under the parent its slash-scope names, in pre-order", () => {
    const tree = run([
      sa("researcher", 1, { kind: "turn-begin" }),
      sa("writer", 1, { kind: "turn-begin" }),
      sa("researcher/summarizer", 2, { kind: "turn-begin" }),
    ]);
    // researcher, then its child summarizer, then the sibling writer.
    expect(tree.map((n) => n.scope)).toEqual(["researcher", "researcher/summarizer", "writer"]);
    expect(tree.map((n) => n.depth)).toEqual([1, 2, 1]);
    expect(tree.map((n) => n.name)).toEqual(["researcher", "summarizer", "writer"]);
  });

  it("creates a placeholder parent when a child event arrives first", () => {
    const tree = run([sa("researcher/summarizer", 2, { kind: "turn-begin" })]);
    expect(tree.map((n) => n.scope)).toEqual(["researcher", "researcher/summarizer"]);
    // The synthesized parent sits at depth 1, idle until its own event lands.
    expect(tree[0]).toMatchObject({ depth: 1, status: "idle" });
  });

  it("derives status and counts tool calls from the inner event stream", () => {
    const tree = run([
      sa("researcher", 1, { kind: "turn-begin" }),
      sa("researcher", 1, { kind: "tool-begin", toolCall: { id: "c1", name: "search" } }),
      sa("researcher", 1, { kind: "tool-begin", toolCall: { id: "c2", name: "fetch" } }),
      sa("researcher", 1, { kind: "turn-end", result: { text: "the summary" } }),
    ]);
    expect(tree).toHaveLength(1);
    expect(tree[0]).toMatchObject({ status: "done", toolCalls: 2, resultText: "the summary" });
  });

  it("ignores non-sub-agent frames", () => {
    const tree = run([{ Kind: "runner-event", RunnerEvent: { kind: "text-delta", text: "hi" } }]);
    expect(tree).toEqual([]);
  });
});
