import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { GRPCWSClient } from "@panyam/servicekit-client";
import { base64Encode } from "@bufbuild/protobuf/wire";
import type { Client } from "@modelcontextprotocol/client";
import { joinToolRoom, wsUrl, MAX_EVENT_DATA_BYTES, type PageTool, type ToolRoom } from "../src/index.js";
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

/** A bare relay peer that speaks the page's wire by hand, addressed. */
async function rawPeer(username: string) {
  const grpc = new GRPCWSClient();
  const inbox: any[] = [];
  const waiters: Array<() => void> = [];
  let joined!: () => void;
  const ready = new Promise<void>((r) => (joined = r));
  grpc.onMessage = (data: any) => {
    if (data?.roomJoined) joined();
    const am = data?.appMessage;
    if (am?.kind === "mcp/1") {
      inbox.push(JSON.parse(Buffer.from(am.payload, "base64").toString()));
      for (const w of waiters.splice(0)) w();
    }
  };
  await grpc.connect(wsUrl(info.relay, ROOM, info.pageTokens.alice));
  cleanup.push(() => grpc.close());
  grpc.send({ join: { sessionId: ROOM, username, clientType: "browser", protocolVersion: 2 } });
  await ready;
  return {
    send(to: string, m: unknown) {
      grpc.send({ appMessage: { kind: "mcp/1", payload: base64Encode(new TextEncoder().encode(JSON.stringify(m))), toClientIds: [to] } });
    },
    async next(match: (m: any) => boolean): Promise<unknown> {
      for (;;) {
        const hit = inbox.find(match);
        if (hit) return hit;
        await Promise.race([new Promise<void>((r) => waiters.push(r)), new Promise((_, rej) => setTimeout(() => rej(new Error("no reply")), 3000))]);
      }
    },
    close: () => grpc.close(),
  };
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
    expect(pages.find((x) => x.page === p.id)).toMatchObject({ user: "alice", label: "board.kicad_sch", allowed: true, default: true });

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

  // A reply addressed to a bridge that has since left gets ErrorEvent
  // PEER_NOT_FOUND from the relay. That refuses one message, so the page has to
  // stay in the room and keep answering everyone else.
  it("stays in the room when it answers a caller that already left", async () => {
    let release!: () => void;
    const p = await page("alice", { name: "slow_once", execute: () => new Promise<string>((r) => (release = () => r("late"))) });
    let closed = false;
    void p.closed.then(() => (closed = true));

    const caller = await rawPeer("caller");
    caller.send(p.id, { jsonrpc: "2.0", id: 1, method: "tools/call", params: { name: "slow_once", arguments: {} } });
    await eventually(() => release !== undefined, 3000, "slow_once started");
    caller.close();
    await new Promise((r) => setTimeout(r, 300));
    release();
    await new Promise((r) => setTimeout(r, 300));

    const other = await rawPeer("other");
    other.send(p.id, { jsonrpc: "2.0", id: 7, method: "tools/list" });
    const reply: any = await other.next((m) => m.id === 7);
    expect(reply.result.tools.map((t: any) => t.name)).toEqual(["slow_once"]);
    expect(closed).toBe(false);
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

  it("delivers ask, selection and its own events to the bridge, stamped with the page's identity", async () => {
    const p = await page("alice");
    p.setLabel("viewer");
    const a = await agent("alice");
    await eventually(async () => (await listPages(a)).some((x) => x.page === p.id), 5000, "the page to be listed");

    p.ask("why is SDA flagged?");
    p.selectionChanged({ net: "SDA" });
    p.emit("viewer.sheet_opened", { sheet: "s-power" });

    const mine = async () => {
      const res: any = await a.callTool({ name: "teststack_page_events", arguments: {} });
      return (res.structuredContent?.events ?? []).filter((e: any) => e.page === p.id);
    };
    await eventually(async () => (await mine()).length === 3, 5000, "three page events at the bridge");
    const got = await mine();
    expect(got.map((e: any) => e.name)).toEqual(["room.ask", "page.selection_changed", "viewer.sheet_opened"]);
    for (const e of got) expect(e).toMatchObject({ resource: ROOM, subject: "alice", label: "viewer" });
    expect(got[0].data).toEqual({ text: "why is SDA flagged?" });
    expect(got[1].data).toEqual({ selection: { net: "SDA" } });
  });

  it("refuses an event name or size the bridge would drop, before sending", async () => {
    const p = await page("alice");
    expect(() => p.emit("Room Ask", null)).toThrow(/invalid page event name/);
    expect(() => p.emit("room.ask", "x".repeat(MAX_EVENT_DATA_BYTES))).toThrow(/over/);
  });

  it("pairs another user's agent by the code the page shows, once, and tells the page who it was", async () => {
    const p = await page("alice", showSheet);
    const paired: any[] = [];
    p.onPaired((n) => paired.push(n));
    const bob = await agent("bob");
    const bobAgain = await agent("bob");
    await eventually(async () => (await listPages(bob)).some((x) => x.page === p.id), 5000, "the page to be listed");

    const code = p.offerPairingCode();
    expect(code).toMatch(/^[A-HJ-NP-Z2-9]{8}$/);
    // The offer travels page to bridge; give it a moment rather than spend
    // the session's guesses retrying.
    await new Promise((r) => setTimeout(r, 300));
    const res: any = await bob.callTool({ name: "pair", arguments: { resource: ROOM, code } });
    expect(res.isError, textOf(res)).toBeFalsy();
    const ran: any = await bob.callTool({ name: "show_sheet", arguments: { sheet: "s-power" } });
    expect(textOf(ran)).toBe("showing s-power");

    await eventually(() => paired.length === 1, 5000, "onPaired");
    expect(paired[0]).toMatchObject({ subject: "bob", sameSubject: false, code });

    const again: any = await bobAgain.callTool({ name: "pair", arguments: { resource: ROOM, code } });
    expect(again.isError).toBe(true);
    expect(textOf(again)).toMatch(/single-use/);
  });

  it("answers next_request with what the page's user did, for agents that speak no events", async () => {
    const p = await page("alice");
    p.setAgentsAllowed(true);
    const a = await agent("alice");
    await eventually(async () => (await listPages(a)).some((x) => x.page === p.id && x.allowed), 5000, "the page to agree");

    const waiting = a.callTool({ name: "next_request", arguments: { wait_seconds: 5 } });
    await new Promise((r) => setTimeout(r, 200));
    p.ask("why is SDA flagged?");
    const res: any = await waiting;
    expect(res.isError, textOf(res)).toBeFalsy();
    expect(res.structuredContent.event).toMatchObject({ name: "room.ask", resource: ROOM, page: p.id, user: "alice", data: { text: "why is SDA flagged?" } });

    // An event between calls comes back on the next call, from the cursor.
    p.selectionChanged({ net: "SCL" });
    await new Promise((r) => setTimeout(r, 200));
    const next: any = await a.callTool({ name: "next_request", arguments: { after: res.structuredContent.cursor, wait_seconds: 5 } });
    expect(next.structuredContent.event).toMatchObject({ name: "page.selection_changed", data: { selection: { net: "SCL" } } });
  });

  it("tells a subscribed agent its room changed, through notifications/resources/updated", async () => {
    const p = await page("alice");
    p.setAgentsAllowed(true);
    const a = await connectAgent(info, "alice");
    cleanup.push(() => a.close());
    const updated: string[] = [];
    a.setNotificationHandler("notifications/resources/updated", (n: any) => {
      updated.push(n.params.uri);
    });
    const attached: any = await a.callTool({ name: "attach", arguments: { resource: ROOM } });
    const uri = attached.structuredContent.eventsResource;
    expect(uri).toBe(`rooms://events/${encodeURIComponent(ROOM).replace(/%20/g, "+")}`);
    await a.subscribeResource({ uri });
    await eventually(async () => (await listPages(a)).some((x) => x.page === p.id && x.allowed), 5000, "the page to agree");

    p.emit("viewer.sheet_opened", { sheet: "s-power" });
    await eventually(() => updated.length === 1, 5000, "resources/updated");
    expect(updated).toEqual([uri]);
    const read: any = await a.readResource({ uri });
    const body = JSON.parse(read.contents[0].text);
    expect(body.events.map((e: any) => e.name)).toContain("viewer.sheet_opened");
  });

  it("grants another user's agent by accepting its code, keeps the grant past its session, and revokes it", async () => {
    const p = await page("alice", showSheet);
    const first = await connectAgent(info, "bob");
    const attached: any = await first.callTool({ name: "attach", arguments: { resource: ROOM } });
    p.acceptPairing(attached.structuredContent.pairingCode);
    await eventually(() => p.grants().some((g) => g.subject === "bob"), 5000, "the page to grant bob");
    await first.close();

    // A new session, no attach: the room on the call and the page's grant are enough.
    const bob = await connectAgent(info, "bob");
    cleanup.push(() => bob.close());
    await bob.callTool({ name: "list_pages", arguments: { resource: ROOM } });
    const ran: any = await bob.callTool({ name: "show_sheet", arguments: { room: ROOM, page: p.id, sheet: "s-io" } });
    expect(ran.isError, textOf(ran)).toBeFalsy();
    expect(textOf(ran)).toBe("showing s-io");

    p.revoke({ subject: "bob" });
    expect(p.grants()).toEqual([]);
    await eventually(async () => {
      const res: any = await bob.callTool({ name: "show_sheet", arguments: { room: ROOM, page: p.id, sheet: "x" } });
      return !!res.isError && /has not agreed to you/.test(textOf(res));
    }, 5000, "the revoke to reach the bridge");
  });

  it("reaches the tab its user focused last when a call names only the room", async () => {
    const p1 = await page("alice", { ...showSheet, execute: ({ sheet }) => `p1 ${sheet}` });
    const p2 = await page("alice", { ...showSheet, execute: ({ sheet }) => `p2 ${sheet}` });
    p1.setAgentsAllowed(true);
    p2.setAgentsAllowed(true);
    p2.focus();
    const a = await connectAgent(info, "alice");
    cleanup.push(() => a.close());
    await a.callTool({ name: "list_pages", arguments: { resource: ROOM } });
    let res: any;
    await eventually(async () => {
      res = await a.callTool({ name: "show_sheet", arguments: { room: ROOM, sheet: "power" } });
      return !res.isError;
    }, 5000, "the call to land");
    expect(textOf(res)).toBe("p2 power");
  });

  it("passes the caller's principal to execute", async () => {
    const callers: unknown[] = [];
    const p = await page("alice", { ...showSheet, execute: (_args, ctx) => (callers.push(ctx.caller), "ok") });
    p.setAgentsAllowed(true);
    const a = await agent("alice");
    await eventually(async () => {
      const res: any = await a.callTool({ name: "show_sheet", arguments: { room: ROOM, sheet: "x" } });
      return !res.isError;
    }, 5000, "the call to land");
    expect(callers[0]).toEqual({ subject: "alice" });
  });

  it("rejects a join the relay refuses", async () => {
    await expect(joinToolRoom({ relay: info.relay, room: ROOM, token: "not-a-jwt" })).rejects.toThrow();
  });

  it("hears which agents have driven it, merged into one entry per principal", async () => {
    const p = await page("alice", showSheet);
    p.setAgentsAllowed(true);
    const heard: any[][] = [];
    p.onPresence((list) => heard.push(list));
    const a = await agent("alice");
    await eventually(() => p.presence().some((e) => e.subject === "alice"), 5000, "attach reported");
    expect(p.presence().find((e) => e.subject === "alice")?.acting).toBe(false);

    await waitPaired(a, p.id);
    const res: any = await a.callTool({ name: "show_sheet", arguments: { sheet: "x" } });
    expect(res.isError).toBeFalsy();
    await eventually(() => p.presence().find((e) => e.subject === "alice")?.acting === true, 5000, "call reported as acting");
    expect(p.presence().filter((e) => e.subject === "alice")).toHaveLength(1);
    expect(heard.length).toBeGreaterThan(0);
    const last = p.presence()[0];
    expect(last.lastSeen).toBeInstanceOf(Date);
    expect(Date.now() - last.lastSeen.getTime()).toBeLessThan(60_000);
  });

  it("keeps acting, the latest lastSeen and the page's own clock when two reports disagree", async () => {
    const p = await page("alice", showSheet);
    const bridge = await rawPeer("bridge-2");
    const presence = (agents: unknown[]) => ({ jsonrpc: "2.0", method: "notifications/rooms/presence", params: { agents } });
    const earlier = new Date(Date.now() - 10_000).toISOString();
    bridge.send(p.id, presence([{ subject: "carol", client: "cursor", lastSeen: new Date(Date.now() - 1_000).toISOString(), acting: true }]));
    const mine = () => p.presence().filter((e) => e.subject === "carol" || e.subject === "dave");
    await eventually(() => mine().length === 1, 5000, "first report");
    bridge.send(p.id, presence([
      { subject: "carol", client: "cursor", lastSeen: earlier, acting: false },
      { subject: "dave", lastSeen: new Date(Date.now() + 3_600_000).toISOString(), acting: false },
    ]));
    await eventually(() => mine().length === 2, 5000, "second report");

    const [carol, dave] = mine();
    expect(carol).toMatchObject({ subject: "carol", client: "cursor", acting: true });
    expect(carol.lastSeen.getTime()).toBeGreaterThan(Date.parse(earlier));
    expect(dave.lastSeen.getTime()).toBeLessThanOrEqual(Date.now());
  });

  it("hands the host's notifications to the handler for their name, only on the tabs selected", async () => {
    const alices = await page("alice", showSheet);
    const bobs = await page("bob", showSheet);
    const heard: string[] = [];
    alices.onNotify("design.changed", (data) => heard.push(`alice:${JSON.stringify(data)}`));
    alices.onNotify("other.thing", () => heard.push("alice:other"));
    bobs.onNotify("design.changed", (data) => heard.push(`bob:${JSON.stringify(data)}`));
    const host = await agent("alice");
    await eventually(async () => (await listPages(host)).filter((p) => p.page === alices.id || p.page === bobs.id).length === 2, 5000, "both tabs listed");

    const res: any = await host.callTool({ name: "teststack_notify", arguments: { rooms: [ROOM], users: ["alice"], name: "design.changed", data: { rev: 7 } } });
    expect(res.isError).toBeFalsy();
    await eventually(() => heard.length > 0, 5000, "notification delivered");
    await new Promise((r) => setTimeout(r, 200));
    expect(heard).toEqual(['alice:{"rev":7}']);
  });
});
