import { describe, expect, it } from "vitest";
import { decodeFrame } from "./watch.js";

const enc = (o: unknown) => new TextEncoder().encode(JSON.stringify(o));

describe("decodeFrame", () => {
  it("skips the ready sentinel (empty kind)", () => {
    expect(decodeFrame({ kind: "", payload: new Uint8Array() })).toBeNull();
  });

  it("decodes a kinded frame's JSON payload into a HostEvent", () => {
    const ev = decodeFrame({
      kind: "runner-event",
      payload: enc({ Kind: "runner-event", RunnerEvent: { kind: "text-delta", text: "hi" } }),
    });
    expect(ev?.Kind).toBe("runner-event");
    expect(ev?.RunnerEvent?.text).toBe("hi");
  });

  it("returns a minimal {Kind} event when the payload is empty but the kind is set", () => {
    const ev = decodeFrame({ kind: "turn-done", payload: new Uint8Array() });
    expect(ev).toEqual({ Kind: "turn-done" });
  });

  it("never throws on a malformed payload — falls back to {Kind}", () => {
    const ev = decodeFrame({ kind: "message", payload: new TextEncoder().encode("{not json") });
    expect(ev).toEqual({ Kind: "message" });
  });

  it("carries the elicit ask id and message through", () => {
    const ev = decodeFrame({
      kind: "elicit-request",
      payload: enc({ Kind: "elicit-request", AskID: 7, Elicit: { message: "approve do_it?" } }),
    });
    expect(ev?.AskID).toBe(7);
    expect(ev?.Elicit?.message).toBe("approve do_it?");
  });
});
