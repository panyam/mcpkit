import { describe, expect, it } from "vitest";
import { createRoot } from "solid-js";
import { createConversationStore } from "./conversation.js";
import type { HostEvent } from "./hostevent.js";

// The store only needs submit / respondToAsk off the client for the ingest path,
// which these tests do not exercise, so a bare stub satisfies the type.
const stubClient = {} as Parameters<typeof createConversationStore>[0];

function runIngest(events: HostEvent[]) {
  return createRoot((dispose) => {
    const store = createConversationStore(stubClient);
    for (const ev of events) store.ingest(ev);
    const out = {
      turns: store.turns(),
      streaming: store.streaming(),
      ask: store.ask(),
      error: store.error(),
      session: store.status().session,
    };
    dispose();
    return out;
  });
}

describe("ConversationStore.ingest", () => {
  it("folds text-delta frames into a committed assistant turn at turn-end", () => {
    const out = runIngest([
      { Kind: "runner-event", RunnerEvent: { kind: "text-delta", text: "Hello " } },
      { Kind: "runner-event", RunnerEvent: { kind: "text-delta", text: "there" } },
      { Kind: "runner-event", RunnerEvent: { kind: "turn-end" } },
    ]);
    expect(out.turns).toEqual([{ role: "assistant", text: "Hello there" }]);
    expect(out.streaming).toBe("");
  });

  it("commits the streamed text on a turn-done frame too", () => {
    const out = runIngest([
      { Kind: "runner-event", RunnerEvent: { kind: "text-delta", text: "done" } },
      { Kind: "turn-done" },
    ]);
    expect(out.turns).toEqual([{ role: "assistant", text: "done" }]);
  });

  it("raises a pending ask on elicit-request and retracts it on elicit-resolved", () => {
    const asked = runIngest([{ Kind: "elicit-request", AskID: 5, Elicit: { message: "approve?" } }]);
    expect(asked.ask).toEqual({ id: 5, message: "approve?" });

    const resolved = runIngest([
      { Kind: "elicit-request", AskID: 5, Elicit: { message: "approve?" } },
      { Kind: "elicit-resolved", AskID: 5, By: "tui" },
    ]);
    expect(resolved.ask).toBeNull();
  });

  it("records a turn-failed error and tracks the active session", () => {
    const out = runIngest([
      { Kind: "session-changed", RunID: "run-123" },
      { Kind: "turn-failed", Err: "boom" },
    ]);
    expect(out.session).toBe("run-123");
    expect(out.error).toBe("boom");
  });
});
