import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { JSDOM } from "jsdom";
import { afterAll, beforeAll, expect, it } from "vitest";
import { joinToolRoom, registerOnApp, type PageTool } from "../src/index.js";
import { connectAgent, ROOM, startStack, textOf, waitPaired } from "./stack.js";

// Acceptance 7 of issue 1581: the same page tool code registers in an MCP
// Apps frame (ext/ui's mcp-app-bridge, as a host frames it) and in a relay
// room, and an agent calls it both ways.

const BRIDGE_JS = join(dirname(fileURLToPath(import.meta.url)), "../../../../../ext/ui/assets/mcp-app-bridge.js");

let stack: Awaited<ReturnType<typeof startStack>>;
beforeAll(async () => {
  stack = await startStack();
});
afterAll(() => stack?.stop());

/** One tool, written once, offered both ways below. */
const calls: string[] = [];
const showSheet: PageTool = {
  name: "show_sheet",
  description: "Open a sheet of the design on screen",
  inputSchema: { type: "object", properties: { sheet: { type: "string" } }, required: ["sheet"] },
  execute: ({ sheet }) => {
    calls.push(String(sheet));
    return { content: [{ type: "text", text: `showing ${sheet}` }] };
  },
};

/**
 * Loads ext/ui's app bridge into a jsdom window of its own, as a host's
 * iframe would hold it, with the window's parent standing in for the host.
 * Returns the frame's MCPApp and a host that sends JSON-RPC to the frame and
 * collects what it posts back. The room half runs outside this window, on
 * Node's own WebSocket.
 */
async function frameHost() {
  const dom = new JSDOM("<!doctype html><html><body></body></html>", { runScripts: "outside-only" });
  const win = dom.window as any;
  const sent: any[] = [];
  Object.defineProperty(win, "parent", { value: { postMessage: (msg: any) => sent.push(msg) }, configurable: true });
  win.eval(readFileSync(BRIDGE_JS, "utf-8")); // the bridge is an IIFE setting window.MCPApp
  const fromHost = (data: any) => {
    const ev = new win.MessageEvent("message", { data, origin: "*" });
    Object.defineProperty(ev, "source", { value: win.parent });
    win.dispatchEvent(ev);
  };
  await vi_waitFor(() => sent.some((m) => m.method === "ui/initialize"));
  const init = sent.find((m) => m.method === "ui/initialize");
  fromHost({
    jsonrpc: "2.0",
    id: init.id,
    result: { protocolVersion: "2026-01-26", hostInfo: { name: "test-host", version: "0" }, hostCapabilities: {}, hostContext: {} },
  });
  await vi_waitFor(() => win.MCPApp?.connected === true);
  return {
    app: win.MCPApp,
    close: () => dom.window.close(),
    async call(id: number, method: string, params: any) {
      fromHost({ jsonrpc: "2.0", id, method, params });
      await vi_waitFor(() => sent.some((m) => m.id === id && (m.result || m.error)));
      return sent.find((m) => m.id === id && (m.result || m.error));
    },
  };
}

async function vi_waitFor(ok: () => boolean, timeoutMs = 3000) {
  const start = Date.now();
  while (!ok()) {
    if (Date.now() - start > timeoutMs) throw new Error("timed out");
    await new Promise((r) => setTimeout(r, 10));
  }
}

it("the same tool runs in an MCP Apps frame and in a relay room", async () => {
  // In the frame: the host lists and calls it over postMessage.
  const host = await frameHost();
  registerOnApp(host.app, showSheet);
  const listed = await host.call(9001, "tools/list", {});
  expect(listed.result.tools.map((t: any) => t.name)).toContain("show_sheet");
  const viaFrame = await host.call(9002, "tools/call", { name: "show_sheet", arguments: { sheet: "s-power" } });
  expect(viaFrame.result.content).toEqual([{ type: "text", text: "showing s-power" }]);
  host.close();

  // In a room: an agent calls it through the Go bridge and the relay.
  const { info } = stack;
  const page = await joinToolRoom({ relay: info.relay, room: ROOM, token: info.pageTokens.alice });
  page.registerTool(showSheet);
  page.setAgentsAllowed(true);
  const agent = await connectAgent(info, "alice");
  try {
    await agent.callTool({ name: "attach", arguments: { resource: ROOM } });
    await waitPaired(agent, page.id);
    const viaRoom: any = await agent.callTool({ name: "show_sheet", arguments: { sheet: "s-io" } });
    expect(viaRoom.isError).toBeFalsy();
    expect(textOf(viaRoom)).toBe("showing s-io");
  } finally {
    await agent.close();
    page.leave();
  }

  expect(calls).toEqual(["s-power", "s-io"]);
});
