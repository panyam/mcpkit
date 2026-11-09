import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { GRPCWSClient } from "@panyam/servicekit-client";
import { base64Encode } from "@bufbuild/protobuf/wire";
import { joinToolRoom, wsUrl, MESSAGE_KIND, type PageTool } from "../src/index.js";
import { startStack, type StackInfo } from "./stack.js";

// The page side of WIRE.md, replayed from testdata/wire/page.jsonl, the same
// file the Go page replays (wire_fixture_test.go). The test plays the bridge
// as a bare relay peer in a room the Go bridge never joins.

const FIXTURE = join(dirname(fileURLToPath(import.meta.url)), "../../testdata/wire/page.jsonl");
const ROOM = "fixture:wire";

let stack: Awaited<ReturnType<typeof startStack>>;
let info: StackInfo;
beforeAll(async () => {
  stack = await startStack();
  info = stack.info;
});
afterAll(() => stack?.stop());

/** Whether got carries everything want does; "$ANY" matches any value present. */
export function wireSubset(want: unknown, got: unknown): boolean {
  if (want === "$ANY") return got !== undefined;
  if (Array.isArray(want)) {
    return Array.isArray(got) && got.length === want.length && want.every((w, i) => wireSubset(w, got[i]));
  }
  if (want && typeof want === "object") {
    if (!got || typeof got !== "object" || Array.isArray(got)) return false;
    return Object.entries(want).every(([k, w]) => k in (got as object) && wireSubset(w, (got as any)[k]));
  }
  return JSON.stringify(want) === JSON.stringify(got);
}

/** A bare relay peer that plays the bridge: it sends raw frames and collects the page's. */
async function bridgePeer() {
  const grpc = new GRPCWSClient();
  const inbox: unknown[] = [];
  let wake: (() => void) | undefined;
  let joined!: () => void;
  const ready = new Promise<void>((r) => (joined = r));
  grpc.onMessage = (data: any) => {
    if (data?.roomJoined) joined();
    const am = data?.appMessage;
    if (am?.kind === MESSAGE_KIND) {
      inbox.push(JSON.parse(Buffer.from(am.payload, "base64").toString()));
      wake?.();
    }
  };
  await grpc.connect(wsUrl(info.relay, ROOM, info.pageTokens.bob));
  grpc.send({ join: { sessionId: ROOM, username: "fixture-bridge", clientType: "api", protocolVersion: 2 } });
  await ready;
  return {
    send(to: string, m: unknown) {
      grpc.send({ appMessage: { kind: MESSAGE_KIND, payload: base64Encode(new TextEncoder().encode(JSON.stringify(m))), toClientIds: [to] } });
    },
    async next(timeoutMs: number): Promise<unknown | undefined> {
      if (inbox.length) return inbox.shift();
      await new Promise<void>((r) => {
        const t = setTimeout(r, timeoutMs);
        wake = () => (clearTimeout(t), r());
      });
      wake = undefined;
      return inbox.shift();
    },
    close: () => grpc.close(),
  };
}

function fixtureTool(t: any): PageTool {
  return {
    name: t.name,
    description: t.description,
    inputSchema: t.inputSchema,
    annotations: { readOnlyHint: t.readOnly },
    execute: ({ sheet }) => `showing ${sheet}`,
  };
}

describe("the wire fixture", () => {
  it("the TypeScript page replays page.jsonl", async () => {
    const steps = readFileSync(FIXTURE, "utf8").trim().split("\n").map((l) => JSON.parse(l));
    const bridge = await bridgePeer();
    const page = await joinToolRoom({ relay: info.relay, room: ROOM, token: info.pageTokens.alice });
    try {
      for (const [i, s] of steps.entries()) {
        const where = `step ${i + 1}`;
        if (s.note !== undefined) continue;
        if (s.page) {
          switch (s.page) {
            case "register_tool":
              page.registerTool(fixtureTool(s.tool));
              break;
            case "declare_event":
              page.registerEvent(s.decl);
              break;
            case "set_label":
              page.setLabel(s.label);
              break;
            case "set_agents_allowed":
              page.setAgentsAllowed(s.allowed);
              break;
            case "emit":
              page.emit(s.name, s.data);
              break;
            case "accept_pairing":
              page.acceptPairing(s.code);
              break;
            default:
              throw new Error(`${where}: unknown page action ${s.page}`);
          }
        } else if (s.bridge) {
          bridge.send(page.id, s.bridge);
        } else if (s.page_sends) {
          const got = await bridge.next(2000);
          expect(got, `${where}: the page sent nothing, want ${JSON.stringify(s.page_sends)}`).toBeDefined();
          expect(wireSubset(s.page_sends, got), `${where}:\n want ${JSON.stringify(s.page_sends)}\n  got ${JSON.stringify(got)}`).toBe(true);
        } else if (s.page_sends_nothing) {
          const got = await bridge.next(300);
          expect(got, `${where}: the page sent ${JSON.stringify(got)}`).toBeUndefined();
        } else {
          throw new Error(`${where}: empty step`);
        }
      }
    } finally {
      page.leave();
      bridge.close();
    }
  });

  it("matches as the Go replay does", () => {
    expect(wireSubset({ a: 1 }, { a: 1, b: 2 })).toBe(true);
    expect(wireSubset({ a: 1 }, { a: "1" })).toBe(false);
    expect(wireSubset({ a: "$ANY" }, {})).toBe(false);
    expect(wireSubset([1, 2], [1, 2, 3])).toBe(false);
  });
});
