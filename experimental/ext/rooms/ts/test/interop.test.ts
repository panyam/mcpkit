import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { GRPCWSClient } from "@panyam/servicekit-client";
import { base64Encode } from "@bufbuild/protobuf/wire";
import type { Client } from "@modelcontextprotocol/client";
import { joinToolRoom, wsUrl, type PageTool, type ToolRoom } from "../src/index.js";
import { connectAgent, eventually, listPages, ROOM, startStack, textOf, waitPaired, type StackInfo } from "./stack.js";

// The Go room bridge (experimental/ext/rooms) on a real massrelay relay, called
// by a TypeScript MCP agent, running tools a TypeScript page offers through
// joinToolRoom. Every hop crosses the language boundary.

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

async function page(subject: string, ...tools: PageTool[]): Promise<ToolRoom> {
  const p = await joinToolRoom({ relay: info.relay, room: ROOM, token: info.pageTokens[subject] });
  cleanup.push(() => p.leave());
  for (const t of tools) p.registerTool(t);
  return p;
}

async function agent(subject: string): Promise<Client> {
  const a = await connectAgent(info, subject);
  cleanup.push(() => a.close());
  await a.callTool({ name: "attach", arguments: { resource: ROOM } });
  return a;
}

const showSheet: PageTool = {
  name: "show_sheet",
  description: "Open a sheet of the design on screen",
  inputSchema: { type: "object", properties: { sheet: { type: "string" } }, required: ["sheet"] },
  execute: ({ sheet }) => `showing ${sheet}`,
};

/** A tool that reports when it starts and whether its signal was aborted. */
function slowTool() {
  let started!: () => void;
  let aborted!: () => void;
  const tool: PageTool = {
    name: "slow",
    execute: (_args, ctx) =>
      new Promise(() => {
        started();
        ctx.signal?.addEventListener("abort", () => aborted());
      }),
  };
  return {
    tool,
    started: new Promise<void>((r) => (started = r)),
    aborted: new Promise<void>((r) => (aborted = r)),
  };
}

describe("a TypeScript page behind the Go bridge", () => {
  it("runs a tool for its own user's agent, and the roster carries the token's subject", async () => {
    const p = await page("alice", showSheet);
    p.setLabel("board.kicad_sch");
    p.setAgentsAllowed(true);
    const a = await agent("alice");
    await waitPaired(a, p.id);

    const pages = await listPages(a);
    expect(pages.find((x) => x.page === p.id)).toMatchObject({ user: "alice", label: "board.kicad_sch", paired: true });

    const res: any = await a.callTool({ name: "show_sheet", arguments: { sheet: "s-power" } });
    expect(res.isError).toBeFalsy();
    expect(textOf(res)).toBe("showing s-power");
  });

  it("refuses another user's agent even with the switch on", async () => {
    const ran: string[] = [];
    await page("alice", { ...showSheet, execute: ({ sheet }) => (ran.push(String(sheet)), "ran") });
    const bob = await agent("bob");
    await eventually(async () => (await bob.listTools()).tools.some((t) => t.name === "show_sheet"), 5000, "show_sheet listed");

    const res: any = await bob.callTool({ name: "show_sheet", arguments: { sheet: "x" } });
    expect(res.isError).toBe(true);
    expect(ran).toEqual([]);
  });

  it("aborts execute's signal when the agent cancels", async () => {
    const s = slowTool();
    const p = await page("alice", s.tool);
    p.setAgentsAllowed(true);
    const a = await agent("alice");
    await waitPaired(a, p.id);

    const ctl = new AbortController();
    const call = a.callTool({ name: "slow", arguments: {} }, { signal: ctl.signal }).catch(() => "cancelled");
    await s.started;
    ctl.abort();
    await s.aborted;
    expect(await call).toBe("cancelled");
  });

  it("fails a pending call at once when the page leaves, naming it", async () => {
    const s = slowTool();
    const p = await page("alice", s.tool);
    p.setAgentsAllowed(true);
    const a = await agent("alice");
    await waitPaired(a, p.id);

    const call = a.callTool({ name: "slow", arguments: {} });
    await s.started;
    const leftAt = Date.now();
    p.leave();
    const res: any = await call;
    expect(Date.now() - leftAt).toBeLessThan(3000);
    expect(res.isError).toBe(true);
    expect(textOf(res)).toContain(p.id);
    expect(textOf(res)).toContain("left the room");
    await s.aborted; // leaving aborts the page's own calls too
  });

  it("relays progress to the agent under its own token", async () => {
    const p = await page("alice", {
      name: "count",
      execute: (_args, ctx) => {
        ctx.progress?.(1, 2, "half");
        ctx.progress?.(2, 2, "done");
        return "counted";
      },
    });
    p.setAgentsAllowed(true);
    const a = await agent("alice");
    await waitPaired(a, p.id);

    const seen: Array<{ progress: number; total?: number; message?: string }> = [];
    const res: any = await a.callTool({ name: "count", arguments: {} }, { onprogress: (pr) => seen.push(pr) });
    expect(textOf(res)).toBe("counted");
    await eventually(() => seen.length === 2, 3000, "two progress notifications");
    expect(seen).toMatchObject([{ progress: 1, total: 2, message: "half" }, { progress: 2, total: 2, message: "done" }]);
  });

  it("shows a tool registered after attach, without reattaching", async () => {
    const p = await page("alice", showSheet);
    const a = await agent("alice");
    await eventually(async () => (await a.listTools()).tools.some((t) => t.name === "show_sheet"), 5000, "show_sheet listed");

    p.registerTool({ name: "highlight_net", execute: () => "ok" });
    await eventually(async () => (await a.listTools()).tools.some((t) => t.name === "highlight_net"), 5000, "highlight_net listed");
  });

  it("ignores room-wide messages, even in the bridge's own format", async () => {
    const ran: string[] = [];
    const p = await page("alice", { ...showSheet, execute: ({ sheet }) => (ran.push(String(sheet)), "ran") });

    const intruder = new GRPCWSClient();
    await intruder.connect(wsUrl(info.relay, ROOM, info.pageTokens.bob));
    cleanup.push(() => intruder.close());
    intruder.send({ join: { sessionId: ROOM, username: "intruder", clientType: "browser", protocolVersion: 2 } });
    await new Promise((r) => setTimeout(r, 200));
    const call = { jsonrpc: "2.0", id: 1, method: "tools/call", params: { name: "show_sheet", arguments: { sheet: "spoofed" } } };
    intruder.send({ appMessage: { kind: "mcp/1", payload: base64Encode(new TextEncoder().encode(JSON.stringify(call))) } });
    await new Promise((r) => setTimeout(r, 300));

    expect(ran).toEqual([]);
    expect(p.id).not.toBe("");
  });

  it("also registers with a WebMCP model context when asked", async () => {
    const registered: string[] = [];
    const unregistered: string[] = [];
    const modelContext = {
      registerTool: (t: PageTool) => {
        registered.push(t.name);
        return { unregister: () => unregistered.push(t.name) };
      },
    };
    const p = await joinToolRoom({ relay: info.relay, room: ROOM, token: info.pageTokens.alice, modelContext });
    cleanup.push(() => p.leave());
    const handle = p.registerTool(showSheet);
    handle.unregister();
    expect(registered).toEqual(["show_sheet"]);
    expect(unregistered).toEqual(["show_sheet"]);
  });

  it("rejects a join the relay refuses", async () => {
    await expect(joinToolRoom({ relay: info.relay, room: ROOM, token: "not-a-jwt" })).rejects.toThrow();
  });
});
