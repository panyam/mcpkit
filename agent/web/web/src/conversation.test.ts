import { describe, expect, it } from "vitest";
import { createRoot } from "solid-js";
import { createConversationStore, type ConversationStore } from "./conversation.js";
import type { HostEvent } from "./hostevent.js";

// The store only needs submit / respondToAsk off the client. Most tests only
// exercise ingest (no client call), so a bare stub satisfies the type; the
// submission and local-answer tests pass an async no-op stub so the write path
// resolves instead of throwing.
const stubClient = {} as Parameters<typeof createConversationStore>[0];
const okClient = {
  submit: async () => ({}),
  respondToAsk: async () => ({}),
} as unknown as Parameters<typeof createConversationStore>[0];

function runIngest(events: HostEvent[]) {
  return createRoot((dispose) => {
    const store = createConversationStore(stubClient);
    for (const ev of events) store.ingest(ev);
    const out = snapshot(store);
    dispose();
    return out;
  });
}

function snapshot(store: ConversationStore) {
  return {
    turns: store.turns(),
    streaming: store.streaming(),
    ask: store.ask(),
    resolved: store.resolved(),
    fromOther: store.fromOther(),
    error: store.error(),
    session: store.status().session,
  };
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

  it("raises a pending ask on elicit-request", () => {
    const out = runIngest([{ Kind: "elicit-request", AskID: 5, Elicit: { message: "approve?" } }]);
    expect(out.ask).toEqual({ id: 5, message: "approve?" });
    expect(out.resolved).toBeNull();
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

describe("ConversationStore multi-surface elicitation retraction", () => {
  it("retracts and shows an 'answered on terminal' receipt when the terminal answers", () => {
    // request shown → resolved by another surface → retracted with a receipt.
    const out = runIngest([
      { Kind: "elicit-request", AskID: 5, Elicit: { message: "approve?" } },
      { Kind: "elicit-resolved", AskID: 5, By: "local" },
    ]);
    expect(out.ask).toBeNull();
    expect(out.resolved).toEqual({ by: "local", text: "answered on terminal" });
  });

  it("names another browser tab when a peer web surface answers", () => {
    const out = runIngest([
      { Kind: "elicit-request", AskID: 7, Elicit: { message: "approve?" } },
      { Kind: "elicit-resolved", AskID: 7, By: "web" },
    ]);
    expect(out.ask).toBeNull();
    expect(out.resolved).toEqual({ by: "web", text: "answered in another browser tab" });
  });

  it("shows no receipt when this surface answered its own ask (local-answer path)", () =>
    createRoot((dispose) => {
      const store = createConversationStore(okClient);
      store.ingest({ Kind: "elicit-request", AskID: 9, Elicit: { message: "approve?" } });
      // Answering optimistically retracts the prompt before the frame lands.
      void store.respondToAsk("accept");
      expect(store.ask()).toBeNull();
      // The resolved frame naming this surface confirms the retract without a
      // cross-surface receipt.
      store.ingest({ Kind: "elicit-resolved", AskID: 9, By: "web" });
      expect(store.ask()).toBeNull();
      expect(store.resolved()).toBeNull();
      dispose();
    }));

  it("shows a terminal receipt when this surface clicked but the terminal won the race", () =>
    createRoot((dispose) => {
      const store = createConversationStore(okClient);
      store.ingest({ Kind: "elicit-request", AskID: 11, Elicit: { message: "approve?" } });
      void store.respondToAsk("accept"); // this tab tried
      // ...but the terminal's answer is what resolved the barrier first.
      store.ingest({ Kind: "elicit-resolved", AskID: 11, By: "local" });
      expect(store.ask()).toBeNull();
      expect(store.resolved()).toEqual({ by: "local", text: "answered on terminal" });
      dispose();
    }));

  it("clears a stale receipt when a fresh ask arrives", () => {
    const out = runIngest([
      { Kind: "elicit-request", AskID: 1, Elicit: { message: "one?" } },
      { Kind: "elicit-resolved", AskID: 1, By: "local" },
      { Kind: "elicit-request", AskID: 2, Elicit: { message: "two?" } },
    ]);
    expect(out.ask).toEqual({ id: 2, message: "two?" });
    expect(out.resolved).toBeNull();
  });

  it("ignores a resolved frame for an ask it never tracked", () => {
    const out = runIngest([{ Kind: "elicit-resolved", AskID: 99, By: "local" }]);
    expect(out.resolved).toBeNull();
    expect(out.ask).toBeNull();
  });
});

describe("ConversationStore symmetric turn submission", () => {
  it("tags a turn started on another surface", () => {
    // A turn-begin with no local submit pending is a remote turn.
    const out = runIngest([
      { Kind: "runner-event", RunnerEvent: { kind: "turn-begin" } },
      { Kind: "runner-event", RunnerEvent: { kind: "text-delta", text: "hi from tui" } },
      { Kind: "runner-event", RunnerEvent: { kind: "turn-end" } },
    ]);
    expect(out.turns).toEqual([{ role: "assistant", text: "hi from tui", from: "another surface" }]);
    expect(out.fromOther).toBe(false); // reset after commit
  });

  it("does not tag a turn this surface submitted", () =>
    createRoot((dispose) => {
      const store = createConversationStore(okClient);
      void store.submit("local question"); // credits one pending local turn
      store.ingest({ Kind: "runner-event", RunnerEvent: { kind: "turn-begin" } });
      store.ingest({ Kind: "runner-event", RunnerEvent: { kind: "text-delta", text: "local answer" } });
      store.ingest({ Kind: "runner-event", RunnerEvent: { kind: "turn-end" } });
      expect(store.fromOther()).toBe(false);
      expect(store.turns()).toEqual([
        { role: "user", text: "local question" },
        { role: "assistant", text: "local answer" },
      ]);
      dispose();
    }));

  it("marks the streaming turn while a remote turn is in flight", () =>
    createRoot((dispose) => {
      const store = createConversationStore(stubClient);
      store.ingest({ Kind: "runner-event", RunnerEvent: { kind: "turn-begin" } });
      store.ingest({ Kind: "runner-event", RunnerEvent: { kind: "text-delta", text: "streaming" } });
      expect(store.fromOther()).toBe(true);
      expect(store.streaming()).toBe("streaming");
      dispose();
    }));
});
