import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import type { Client } from "@modelcontextprotocol/client";
import { fetchJoin, joinToolRoomFrom, type PageTool, type ToolRoom } from "../src/index.js";
import { connectAgent, ROOM, startStack, textOf, waitPaired, type StackInfo } from "./stack.js";

// A page that joins through the host's join endpoint (rooms.JoinHandler on a
// massrelay.Local relay), issue 1677. Tokens from /join last 3s here, so the
// refresh happens inside a test.

const TTL_S = 3;
let stack: Awaited<ReturnType<typeof startStack>>;
let info: StackInfo;
const cleanup: Array<() => unknown> = [];

beforeAll(async () => {
  stack = await startStack([`-token-ttl=${TTL_S}s`]);
  info = stack.info;
});
afterAll(() => stack?.stop());
afterEach(async () => {
  for (const fn of cleanup.splice(0).reverse()) await fn();
});

const showSheet: PageTool = {
  name: "show_sheet",
  description: "Open a sheet of the design on screen",
  inputSchema: { type: "object", properties: { sheet: { type: "string" } }, required: ["sheet"] },
  execute: ({ sheet }) => `showing ${sheet}`,
};

function endpoint(user: string, room = ROOM): string {
  return `${info.join}?user=${encodeURIComponent(user)}&room=${encodeURIComponent(room)}`;
}

/** A fetch that counts the calls to the join endpoint. */
function countingFetch(): { fetch: typeof fetch; calls: () => number } {
  let n = 0;
  return {
    fetch: (input, init) => {
      n++;
      return fetch(input, init);
    },
    calls: () => n,
  };
}

async function joined(user: string, f?: typeof fetch): Promise<ToolRoom> {
  const p = await joinToolRoomFrom(endpoint(user), { fetch: f });
  cleanup.push(() => p.leave());
  p.registerTool(showSheet);
  return p;
}

async function agent(subject: string): Promise<Client> {
  const a = await connectAgent(info, subject);
  cleanup.push(() => a.close());
  await a.callTool({ name: "attach", arguments: { resource: ROOM } });
  return a;
}

describe("a page that joins through the join endpoint", () => {
  it("joins the endpoint's room, and its user's agent reaches its tool", async () => {
    const f = countingFetch();
    const p = await joined("alice", f.fetch);
    expect(p.room).toBe(ROOM);
    expect(f.calls()).toBe(1);

    p.setAgentsAllowed(true);
    const a = await agent("alice");
    await waitPaired(a, p.id);
    const res: any = await a.callTool({ name: "show_sheet", arguments: { sheet: "s-power" } });
    expect(res.isError).toBeFalsy();
    expect(textOf(res)).toBe("showing s-power");
  });

  // Acceptance 2 of #1677: the refreshed token keeps the same client id, and
  // the page stays in the room past the first token's expiry.
  it("refreshes its token before expiry and keeps its id", async () => {
    const f = countingFetch();
    const p = await joined("alice", f.fetch);
    const id = p.id;
    let closed = false;
    p.closed.then(() => (closed = true));

    await new Promise((r) => setTimeout(r, (TTL_S + 2) * 1000));
    expect(closed, "the relay ended the page at the first token's exp").toBe(false);
    expect(f.calls()).toBeGreaterThanOrEqual(2);
    expect(p.id).toBe(id);

    p.setAgentsAllowed(true);
    const a = await agent("alice");
    await waitPaired(a, id);
    const res: any = await a.callTool({ name: "show_sheet", arguments: { sheet: "s-io" } });
    expect(textOf(res)).toBe("showing s-io");
  }, 20_000);

  it("rejects with the endpoint's error", async () => {
    await expect(joinToolRoomFrom(endpoint(""))).rejects.toThrow(/401 no user/);
  });
});

describe("fetchJoin", () => {
  const respond = (status: number, body: string): typeof fetch => async () => new Response(body, { status });

  it("POSTs with the page's cookies", async () => {
    let seen: RequestInit | undefined;
    const f: typeof fetch = async (_url, init) => {
      seen = init;
      return new Response(JSON.stringify({ room: "r", relayUrl: "/relay", token: "t", bridgeSubjects: [], expiresAt: "" }));
    };
    await fetchJoin("http://app/join", f, { method: "GET", headers: { "X-Doc": "d" } });
    expect(seen).toMatchObject({ method: "POST", credentials: "same-origin", headers: { "X-Doc": "d" } });
  });

  it("throws on an answer that is not a join response", async () => {
    await expect(fetchJoin("http://app/join", respond(200, "<html>login</html>"))).rejects.toThrow(/not a join response/);
    await expect(fetchJoin("http://app/join", respond(200, "{}"))).rejects.toThrow(/not a join response/);
  });

  it("throws with the status and text of a failed call", async () => {
    await expect(fetchJoin("http://app/join", respond(502, "bad gateway"))).rejects.toThrow(/502 bad gateway/);
  });
});
