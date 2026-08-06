import { describe, expect, it } from "vitest";
import { createRoot } from "solid-js";
import { createToolStore, detectOffload } from "./tools.js";
import type { HostEvent, ToolResult } from "./hostevent.js";

function begin(id: string, name: string, args?: unknown): HostEvent {
  return { Kind: "runner-event", RunnerEvent: { kind: "tool-begin", toolCall: { id, name, args } } };
}
function end(id: string, name: string, result: ToolResult): HostEvent {
  return { Kind: "runner-event", RunnerEvent: { kind: "tool-end", toolCall: { id, name }, toolResult: result } };
}
function text(t: string): ToolResult {
  return { content: [{ type: "text", text: t }] };
}

function run(events: HostEvent[]) {
  return createRoot((dispose) => {
    const store = createToolStore();
    for (const ev of events) store.ingest(ev);
    const out = { calls: store.calls(), offloaded: store.offloadedCount() };
    dispose();
    return out;
  });
}

describe("detectOffload", () => {
  it("extracts the ref from an offload stub, else empty", () => {
    expect(detectOffload("[tool result 715B, stored as res:a4082]\npreview: ...")).toBe("res:a4082");
    expect(detectOffload("a plain small result")).toBe("");
  });
});

describe("ToolStore projection", () => {
  it("matches begin to end by call id and lands a terminal status (new object each time)", () => {
    const first = run([begin("c1", "search", { q: "x" })]);
    expect(first.calls).toHaveLength(1);
    expect(first.calls[0]).toMatchObject({ name: "search", status: "running" });

    const both = run([begin("c1", "search", { q: "x" }), end("c1", "search", text("found 3 rows"))]);
    // Still one row (matched by id), now terminal — and a DIFFERENT object
    // reference than the running one, so a keyed <For> re-renders it.
    expect(both.calls).toHaveLength(1);
    expect(both.calls[0]).toMatchObject({ status: "ok", preview: "found 3 rows" });
  });

  it("flags an offloaded result and surfaces its ref", () => {
    const out = run([
      begin("c2", "report"),
      end("c2", "report", text("[tool result 52KB, stored as res:ab12]\npreview: big output")),
    ]);
    expect(out.calls[0]).toMatchObject({ offloaded: true, ref: "res:ab12" });
    expect(out.offloaded).toBe(1);
  });

  it("maps each terminal kind to its status", () => {
    const out = run([
      begin("a", "t1"),
      { Kind: "runner-event", RunnerEvent: { kind: "tool-error", toolCall: { id: "a", name: "t1" }, error: "boom" } },
      begin("b", "t2"),
      { Kind: "runner-event", RunnerEvent: { kind: "tool-denied", toolCall: { id: "b", name: "t2" }, reason: "no" } },
    ]);
    expect(out.calls.map((c) => c.status)).toEqual(["error", "denied"]);
    expect(out.calls[0].preview).toBe("boom");
  });
});
