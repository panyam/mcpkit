import { describe, expect, it } from "vitest";
import { toCallToolResult, wsUrl } from "../src/index.js";
import { droppedAction } from "../src/sender.js";

describe("wsUrl", () => {
  it("escapes the room into one path segment and carries the token as a query parameter", () => {
    expect(wsUrl("https://relay.example.com/collab/", "design:demo/board.kicad_sch", "t.k.n")).toBe(
      "wss://relay.example.com/collab/ws/v1/design%3Ademo%2Fboard.kicad_sch/sync?token=t.k.n",
    );
  });

  it("keeps ws schemes and omits an empty token", () => {
    expect(wsUrl("ws://127.0.0.1:8787", "review:42")).toBe("ws://127.0.0.1:8787/ws/v1/review%3A42/sync");
  });

  it("refuses other schemes", () => {
    expect(() => wsUrl("ftp://relay", "r")).toThrow(/scheme/);
  });
});

describe("toCallToolResult", () => {
  it("passes a CallToolResult through", () => {
    const r = { content: [{ type: "text", text: "x" }], isError: true };
    expect(toCallToolResult(r)).toBe(r);
  });

  it("sends a string as one text block", () => {
    expect(toCallToolResult("hi")).toEqual({ content: [{ type: "text", text: "hi" }] });
  });

  it("sends other values as text and structuredContent", () => {
    expect(toCallToolResult({ sheet: "s" })).toEqual({ content: [{ type: "text", text: '{"sheet":"s"}' }], structuredContent: { sheet: "s" } });
  });

  it("sends nothing for undefined", () => {
    expect(toCallToolResult(undefined)).toEqual({ content: [] });
  });
});

describe("droppedAction", () => {
  it("reads the action id out of the relay's RATE_LIMITED message", () => {
    expect(droppedAction('Dropped action "p12": over 30 messages/sec')).toBe("p12");
    expect(droppedAction('Dropped action "a \\"quoted\\" id": over 30 messages/sec')).toBe('a "quoted" id');
  });

  it("returns undefined for any other message", () => {
    expect(droppedAction("Not in this room: a, b")).toBeUndefined();
    expect(droppedAction("Dropped action p12")).toBeUndefined();
  });
});
