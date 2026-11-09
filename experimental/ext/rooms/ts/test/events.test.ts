import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import type { Client } from "@modelcontextprotocol/client";
import { joinToolRoom, type EventRefusal, type PageEventDecl, type ToolRoom } from "../src/index.js";
import { connectAgent, eventually, ROOM, startStack, type StackInfo } from "./stack.js";

// A TypeScript page declaring its event types to the Go bridge (#1678).

let stack: Awaited<ReturnType<typeof startStack>>;
let info: StackInfo;
const cleanup: Array<() => unknown> = [];

beforeAll(async () => {
  stack = await startStack();
  info = stack.info;
});
afterAll(() => stack?.stop());
afterEach(async () => {
  for (const fn of cleanup.splice(0).reverse()) await fn();
});

const itemFound: PageEventDecl = {
  name: "item.found",
  description: "The player found an item.",
  payloadSchema: { type: "object", properties: { item: { type: "string" } }, required: ["item"] },
  contextHint: { priority: "high", template: "found {{item}}" },
};

async function page(): Promise<ToolRoom> {
  const p = await joinToolRoom({ relay: info.relay, room: ROOM, token: info.pageTokens.alice });
  cleanup.push(() => p.leave());
  return p;
}

async function agent(): Promise<Client> {
  const a = await connectAgent(info, "alice");
  cleanup.push(() => a.close());
  await a.callTool({ name: "attach", arguments: { resource: ROOM } });
  return a;
}

async function catalog(a: Client): Promise<any[]> {
  const res: any = await a.callTool({ name: "teststack_event_catalog", arguments: {} });
  return res.structuredContent?.events ?? [];
}

async function pageEvents(a: Client): Promise<any[]> {
  const res: any = await a.callTool({ name: "teststack_page_events", arguments: {} });
  return res.structuredContent?.events ?? [];
}

describe("a TypeScript page declaring its events to the Go bridge", () => {
  it("shows up in the bridge's catalog with its schema and hint", async () => {
    const a = await agent();
    const p = await page();
    p.registerEvent(itemFound);

    await eventually(async () => (await catalog(a)).some((e) => e.name === "item.found"), 5000, "item.found in the catalog");
    const got = (await catalog(a)).find((e) => e.name === "item.found");
    expect(got).toMatchObject({
      description: itemFound.description,
      payloadSchema: itemFound.payloadSchema,
      contextHint: itemFound.contextHint,
    });

    p.unregisterEvent("item.found");
    await eventually(async () => !(await catalog(a)).some((e) => e.name === "item.found"), 5000, "item.found gone");
  });

  it("hears why the bridge refused an event, and a valid one gets through", async () => {
    const a = await agent();
    const p = await page();
    const refused: EventRefusal[] = [];
    p.onEventRefused((r) => refused.push(r));
    p.registerEvent(itemFound);
    await eventually(async () => (await catalog(a)).some((e) => e.name === "item.found"), 5000, "item.found in the catalog");

    p.emit("item.found", { item: 7 });
    await eventually(() => refused.length > 0, 5000, "a refusal");
    expect(refused[0].name).toBe("item.found");
    expect(refused[0].reason).toMatch(/does not match the declared payload schema/);

    p.emit("item.found", { item: "brass key" });
    await eventually(
      async () => (await pageEvents(a)).some((e) => e.name === "item.found" && e.data?.item === "brass key"),
      5000,
      "the valid event at the hook",
    );
    expect((await pageEvents(a)).filter((e) => e.name === "item.found")).toHaveLength(1);
  });

  it("rejects an invalid name before anything is sent", async () => {
    const p = await page();
    expect(() => p.registerEvent({ name: "Item Found" })).toThrow(/invalid page event name/);
  });
});
