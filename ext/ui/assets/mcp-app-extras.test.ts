/**
 * Tests for the mcpkit extras used on top of upstream's App (issue 1475).
 *
 * These drive the real App and PostMessageTransport from
 * @modelcontextprotocol/ext-apps, with a scripted fake host standing in for
 * window.parent, so they show the extras working on upstream's runtime rather
 * than on the mcpkit bridge.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { App, PostMessageTransport } from "@modelcontextprotocol/ext-apps";
import { selectFile, selectFiles, withTraceRelay } from "./mcp-app-extras";

const TP = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01";

/** A fake host: records what the View sends and answers requests. */
function fakeHost() {
  const sent: any[] = [];
  const host = {
    postMessage(msg: any) {
      sent.push(msg);
      if (msg.id == null || msg.method == null) return;
      const result =
        msg.method === "ui/initialize"
          ? {
              protocolVersion: "2026-01-26",
              hostInfo: { name: "fake-host", version: "1.0.0" },
              hostCapabilities: { serverTools: {} },
              hostContext: {},
            }
          : msg.method === "tools/call"
            ? { content: [{ type: "text", text: "ok" }] }
            : {};
      setTimeout(() => reply({ jsonrpc: "2.0", id: msg.id, result }), 0);
    },
  };
  function reply(data: unknown) {
    const event = new MessageEvent("message", { data });
    Object.defineProperty(event, "source", { value: host });
    window.dispatchEvent(event);
  }
  return { host, sent };
}

async function connectApp(provider: Parameters<typeof withTraceRelay>[1]) {
  const { host, sent } = fakeHost();
  const transport = withTraceRelay(new PostMessageTransport(host as any, host as any), provider);
  const app = new App({ name: "extras-test", version: "1.0.0" }, {}, { autoResize: false });
  await app.connect(transport);
  sent.length = 0;
  return { app, sent };
}

beforeEach(() => {
  vi.spyOn(console, "debug").mockImplementation(() => {});
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("withTraceRelay on upstream App", () => {
  it("stamps traceparent and tracestate on a tools/call request", async () => {
    const { app, sent } = await connectApp(() => ({ traceparent: TP, tracestate: "vendor=1" }));
    await app.callServerTool({ name: "echo", arguments: { msg: "hi" } });
    const call = sent.find((m) => m.method === "tools/call");
    expect(call.params._meta.traceparent).toBe(TP);
    expect(call.params._meta.tracestate).toBe("vendor=1");
    expect(call.params.name).toBe("echo");
    expect(call.params.arguments).toEqual({ msg: "hi" });
  });

  it("leaves a caller-set traceparent alone", async () => {
    const callerTP = "00-11111111111111111111111111111111-2222222222222222-01";
    const { app, sent } = await connectApp(() => ({ traceparent: TP }));
    await app.callServerTool({ name: "echo", arguments: {}, _meta: { traceparent: callerTP } } as any);
    const call = sent.find((m) => m.method === "tools/call");
    expect(call.params._meta.traceparent).toBe(callerTP);
  });

  it("sends unstamped when the provider returns null", async () => {
    const { app, sent } = await connectApp(() => null);
    await app.callServerTool({ name: "echo", arguments: {} });
    const call = sent.find((m) => m.method === "tools/call");
    expect(call.params._meta?.traceparent).toBeUndefined();
  });

  it("sends unstamped and warns when the provider throws", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const { app, sent } = await connectApp(() => {
      throw new Error("no active span");
    });
    await app.callServerTool({ name: "echo", arguments: { msg: "hi" } });
    const call = sent.find((m) => m.method === "tools/call");
    expect(call.params._meta?.traceparent).toBeUndefined();
    expect(call.params.arguments).toEqual({ msg: "hi" });
    expect(warn).toHaveBeenCalled();
  });

  it("stamps notifications but not responses", async () => {
    const { app, sent } = await connectApp(() => ({ traceparent: TP }));
    app.sendLog({ level: "info", data: "hello" });
    await new Promise((r) => setTimeout(r, 0));
    const note = sent.find((m) => m.method === "notifications/message");
    expect(note.params._meta.traceparent).toBe(TP);

    const transport = withTraceRelay({ send: (m: any) => sent.push(m) }, () => ({ traceparent: TP }));
    sent.length = 0;
    transport.send({ jsonrpc: "2.0", id: 5, result: { ok: true } });
    expect(sent[0]).toEqual({ jsonrpc: "2.0", id: 5, result: { ok: true } });
  });

  it("returns the transport it was given", () => {
    const t = { send: () => {} };
    expect(withTraceRelay(t, () => null)).toBe(t);
  });
});

describe("file picker exported from the extras", () => {
  function stubPicker(file: { name: string; type: string; bytes: number[] }) {
    const realCreate = document.createElement.bind(document);
    vi.spyOn(document, "createElement").mockImplementation(((tag: string) => {
      const el = realCreate(tag) as HTMLInputElement;
      if (tag === "input") {
        el.click = () => {
          const f = new File([new Uint8Array(file.bytes)], file.name, { type: file.type });
          Object.defineProperty(el, "files", { value: [f] });
          el.dispatchEvent(new Event("change"));
        };
      }
      return el;
    }) as typeof document.createElement);
  }

  it("selectFile resolves to the same data URI the bridge produces", async () => {
    stubPicker({ name: "a b.txt", type: "text/plain", bytes: [104, 105] });
    const uri = await selectFile({ accept: ["text/plain"] });
    expect(uri).toBe("data:text/plain;name=a%20b.txt;base64,aGk=");
  });

  it("selectFiles resolves to an array", async () => {
    stubPicker({ name: "x.txt", type: "text/plain", bytes: [120] });
    const uris = await selectFiles();
    expect(uris).toEqual(["data:text/plain;name=x.txt;base64,eA=="]);
  });
});
