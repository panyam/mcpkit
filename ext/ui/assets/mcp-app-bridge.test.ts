/**
 * Unit tests for mcp-app-bridge.js.
 *
 * These run in a jsdom environment that simulates the iframe side.
 * A mock host intercepts postMessage calls and can send messages back.
 *
 * The bridge IIFE is loaded via dynamic import of the compiled JS.
 * We use `new Function()` in the loadBridge helper because the bridge
 * is an IIFE (not an ES module) and must execute in the test's global
 * scope — this is a deliberate test-harness pattern, not production code.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { readFileSync } from "fs";
import { join } from "path";

// --- Test helpers -----------------------------------------------------------

/** Messages sent by the bridge via window.parent.postMessage. */
let sentMessages: any[] = [];

/** Listeners registered on the window for "message" events. */
let messageListeners: ((event: MessageEvent) => void)[] = [];

/** Simulate a message from the host to the iframe. */
function hostSends(data: any) {
  const event = new MessageEvent("message", { data, origin: "*" });
  messageListeners.forEach((fn) => fn(event));
}

/** Auto-respond to ui/initialize with a host context. */
function autoRespondToInitialize() {
  const init = sentMessages.find(
    (m) => m.method === "ui/initialize" && m.id != null
  );
  if (init) {
    hostSends({
      jsonrpc: "2.0",
      id: init.id,
      result: {
        hostContext: { theme: "dark", locale: "en-US" },
        capabilities: { tools: true },
      },
    });
  }
}

/** Wait for MCPApp.connected to become true (with timeout). */
async function waitForConnect(timeoutMs = 3000): Promise<void> {
  const start = Date.now();
  while (!(window as any).MCPApp?.connected) {
    if (Date.now() - start > timeoutMs) {
      throw new Error("Timed out waiting for MCPApp to connect");
    }
    await new Promise((r) => setTimeout(r, 10));
  }
}

/**
 * Load the bridge IIFE into the current jsdom window.
 *
 * Uses `new Function()` intentionally — the bridge is an IIFE that must
 * run in the global scope to set window.MCPApp. This is test-only code;
 * production consumers use `<script>` tags or go:embed.
 */
function loadBridge() {
  const script = readFileSync(join(__dirname, "mcp-app-bridge.js"), "utf-8");
  // Intentional: test harness loading IIFE into jsdom global scope.
  // eslint-disable-next-line no-new-func
  const fn = new Function(script); // NOSONAR — test harness only
  fn();
}

// --- Setup ------------------------------------------------------------------

beforeEach(() => {
  sentMessages = [];
  messageListeners = [];

  // Clean up any prior bridge.
  delete (window as any).MCPApp;

  // Mock window.parent.postMessage — capture outgoing messages.
  Object.defineProperty(window, "parent", {
    value: {
      postMessage: (msg: any, _origin: string) => {
        sentMessages.push(msg);
      },
    },
    writable: true,
    configurable: true,
  });

  // Intercept addEventListener — capture "message" handlers without
  // registering them on the real window (so hostSends is the only path).
  const realAddEventListener = window.addEventListener.bind(window);
  vi.spyOn(window, "addEventListener").mockImplementation(
    (type: string, handler: any, options?: any) => {
      if (type === "message") {
        messageListeners.push(handler);
        return; // Don't register on real window.
      }
      return realAddEventListener(type, handler, options);
    }
  );

  // Stub ResizeObserver (not available in jsdom).
  if (!(globalThis as any).ResizeObserver) {
    (globalThis as any).ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    };
  }

  // Load the bridge.
  loadBridge();
});

// --- Tests ------------------------------------------------------------------

describe("MCPApp global", () => {
  it("exposes MCPApp on window", () => {
    expect((window as any).MCPApp).toBeDefined();
  });

  it("has expected API methods", () => {
    const app = (window as any).MCPApp;
    expect(typeof app.on).toBe("function");
    expect(typeof app.off).toBe("function");
    expect(typeof app.once).toBe("function");
    expect(typeof app.callTool).toBe("function");
    expect(typeof app.readResource).toBe("function");
    expect(typeof app.openLink).toBe("function");
    expect(typeof app.isHosted).toBe("function");
    expect(typeof app.log).toBe("function");
  });

  it("is not connected before handshake", () => {
    expect((window as any).MCPApp.connected).toBe(false);
    expect((window as any).MCPApp.isHosted()).toBe(false);
  });
});

describe("initialize handshake", () => {
  it("sends ui/initialize on load", () => {
    const init = sentMessages.find((m) => m.method === "ui/initialize");
    expect(init).toBeDefined();
    expect(init.jsonrpc).toBe("2.0");
    expect(init.id).toBeDefined();
  });

  it("connects when host responds", async () => {
    autoRespondToInitialize();
    await waitForConnect();

    const app = (window as any).MCPApp;
    expect(app.connected).toBe(true);
    expect(app.isHosted()).toBe(true);
    expect(app.hostContext?.theme).toBe("dark");
  });

  it("emits connected event", async () => {
    const handler = vi.fn();
    (window as any).MCPApp.on("connected", handler);

    autoRespondToInitialize();
    await waitForConnect();

    expect(handler).toHaveBeenCalledOnce();
    expect(handler.mock.calls[0][0].hostContext.theme).toBe("dark");
  });

  it("sends ui/notifications/initialized after connect", async () => {
    autoRespondToInitialize();
    await waitForConnect();

    await new Promise((r) => setTimeout(r, 10));

    const notif = sentMessages.find(
      (m) => m.method === "ui/notifications/initialized"
    );
    expect(notif).toBeDefined();
    expect(notif.id).toBeUndefined();
  });
});

describe("event emitter", () => {
  it("on/off subscribes and unsubscribes", () => {
    const app = (window as any).MCPApp;
    const handler = vi.fn();

    app.on("toolresult", handler);
    hostSends({
      jsonrpc: "2.0",
      method: "ui/notifications/tool-result",
      params: { name: "test", content: [] },
    });
    expect(handler).toHaveBeenCalledOnce();

    app.off("toolresult", handler);
    hostSends({
      jsonrpc: "2.0",
      method: "ui/notifications/tool-result",
      params: { name: "test2", content: [] },
    });
    expect(handler).toHaveBeenCalledOnce();
  });

  it("once fires only once", () => {
    const app = (window as any).MCPApp;
    const handler = vi.fn();

    app.once("toolinput", handler);
    hostSends({
      jsonrpc: "2.0",
      method: "ui/notifications/tool-input",
      params: { name: "t1", arguments: {} },
    });
    hostSends({
      jsonrpc: "2.0",
      method: "ui/notifications/tool-input",
      params: { name: "t2", arguments: {} },
    });
    expect(handler).toHaveBeenCalledOnce();
  });

  it("on returns unsubscribe function", () => {
    const app = (window as any).MCPApp;
    const handler = vi.fn();

    const unsub = app.on("teardown", handler);
    unsub();

    hostSends({
      jsonrpc: "2.0",
      method: "ui/resource-teardown",
      params: {},
    });
    expect(handler).not.toHaveBeenCalled();
  });
});

describe("inbound notifications", () => {
  it("routes tool-input", () => {
    const handler = vi.fn();
    (window as any).MCPApp.on("toolinput", handler);

    hostSends({
      jsonrpc: "2.0",
      method: "ui/notifications/tool-input",
      params: { name: "my_tool", arguments: { x: 1 } },
    });

    expect(handler).toHaveBeenCalledWith({
      tool: "my_tool",
      arguments: { x: 1 },
    });
  });

  it("routes tool-result", () => {
    const handler = vi.fn();
    (window as any).MCPApp.on("toolresult", handler);

    hostSends({
      jsonrpc: "2.0",
      method: "ui/notifications/tool-result",
      params: { name: "my_tool", content: [{ type: "text", text: "hi" }] },
    });

    expect(handler).toHaveBeenCalledOnce();
    expect(handler.mock.calls[0][0].tool).toBe("my_tool");
  });

  it("routes host-context-changed", () => {
    const handler = vi.fn();
    (window as any).MCPApp.on("hostcontextchanged", handler);

    hostSends({
      jsonrpc: "2.0",
      method: "ui/notifications/host-context-changed",
      params: { hostContext: { theme: "light" } },
    });

    expect(handler).toHaveBeenCalledWith({
      hostContext: { theme: "light" },
    });
  });

  it("routes teardown", () => {
    const handler = vi.fn();
    (window as any).MCPApp.on("teardown", handler);

    hostSends({ jsonrpc: "2.0", method: "ui/resource-teardown", params: {} });
    expect(handler).toHaveBeenCalledOnce();
  });

  it("dispatches CustomEvent on document", () => {
    const handler = vi.fn();
    document.addEventListener("mcp:toolresult", handler);

    hostSends({
      jsonrpc: "2.0",
      method: "ui/notifications/tool-result",
      params: { name: "t", content: [] },
    });

    expect(handler).toHaveBeenCalledOnce();
    expect((handler.mock.calls[0][0] as CustomEvent).detail.tool).toBe("t");

    document.removeEventListener("mcp:toolresult", handler);
  });
});

describe("outbound requests", () => {
  beforeEach(async () => {
    autoRespondToInitialize();
    await waitForConnect();
  });

  it("callTool sends JSON-RPC request and resolves on response", async () => {
    const promise = (window as any).MCPApp.callTool("echo", { msg: "hi" });

    await new Promise((r) => setTimeout(r, 10));
    const call = sentMessages.find((m) => m.method === "tools/call");
    expect(call).toBeDefined();
    expect(call.params.name).toBe("echo");
    expect(call.params.arguments.msg).toBe("hi");

    hostSends({
      jsonrpc: "2.0",
      id: call.id,
      result: { content: [{ type: "text", text: "echo: hi" }] },
    });

    const result = await promise;
    expect((result as any).content[0].text).toBe("echo: hi");
  });

  it("callTool rejects on error response", async () => {
    const promise = (window as any).MCPApp.callTool("fail", {});

    await new Promise((r) => setTimeout(r, 10));
    const call = sentMessages.find((m) => m.method === "tools/call");

    hostSends({
      jsonrpc: "2.0",
      id: call.id,
      error: { code: -32000, message: "tool failed" },
    });

    await expect(promise).rejects.toThrow("tool failed");
  });

  it("openLink sends request and resolves on response", async () => {
    const promise = (window as any).MCPApp.openLink("https://example.com");

    await new Promise((r) => setTimeout(r, 10));
    const msg = sentMessages.find((m) => m.method === "ui/open-link");
    expect(msg).toBeDefined();
    expect(msg.params.url).toBe("https://example.com");
    expect(msg.id).toBeDefined(); // request, not notification

    hostSends({ jsonrpc: "2.0", id: msg.id, result: { isError: false } });
    const result = await promise;
    expect((result as any).isError).toBe(false);
  });

  it("log sends notification", async () => {
    (window as any).MCPApp.log("info", "test message", { extra: true });

    await new Promise((r) => setTimeout(r, 10));
    const msg = sentMessages.find((m) => m.method === "ui/log");
    expect(msg).toBeDefined();
    expect(msg.params.level).toBe("info");
    expect(msg.params.message).toBe("test message");
  });
});

describe("style utilities", () => {
  beforeEach(async () => {
    autoRespondToInitialize();
    await waitForConnect();
  });

  it("auto-applies theme on connect", async () => {
    // The auto-respond sends theme: "dark"
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
    expect(document.documentElement.style.colorScheme).toBe("dark");
  });

  it("applyTheme sets data-theme and color-scheme", () => {
    (window as any).MCPApp.applyTheme("light");
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    expect(document.documentElement.style.colorScheme).toBe("light");
  });

  it("applyStyleVariables sets CSS custom properties", () => {
    (window as any).MCPApp.applyStyleVariables({
      "--color-bg": "#fff",
      "--color-text": "#000",
    });
    expect(
      document.documentElement.style.getPropertyValue("--color-bg")
    ).toBe("#fff");
    expect(
      document.documentElement.style.getPropertyValue("--color-text")
    ).toBe("#000");
  });

  it("applyFonts injects style tag once", () => {
    (window as any).MCPApp.applyFonts("@font-face { font-family: Test; }");
    const style = document.getElementById("__mcp-host-fonts");
    expect(style).toBeDefined();
    expect(style?.textContent).toContain("@font-face");

    // Second call is a no-op.
    (window as any).MCPApp.applyFonts("@font-face { font-family: Other; }");
    expect(document.querySelectorAll("#__mcp-host-fonts").length).toBe(1);
  });

  it("auto-applies styles on host-context-changed", () => {
    hostSends({
      jsonrpc: "2.0",
      method: "ui/notifications/host-context-changed",
      params: {
        hostContext: {
          theme: "light",
          styles: { variables: { "--accent": "blue" } },
        },
      },
    });
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    expect(
      document.documentElement.style.getPropertyValue("--accent")
    ).toBe("blue");
  });
});

describe("AbortSignal support", () => {
  beforeEach(async () => {
    autoRespondToInitialize();
    await waitForConnect();
  });

  it("callTool with timeout rejects on timeout", async () => {
    const promise = (window as any).MCPApp.callTool("slow", {}, { timeout: 50 });
    // Don't respond — let it timeout.
    await expect(promise).rejects.toThrow(/abort|timed out/i);
  });

  it("callTool with signal rejects on abort", async () => {
    const controller = new AbortController();
    const promise = (window as any).MCPApp.callTool("test", {}, {
      signal: controller.signal,
    });
    controller.abort(new Error("user cancelled"));
    await expect(promise).rejects.toThrow("user cancelled");
  });

  it("callTool with pre-aborted signal rejects immediately", async () => {
    const signal = AbortSignal.abort(new Error("already aborted"));
    await expect(
      (window as any).MCPApp.callTool("test", {}, { signal })
    ).rejects.toThrow("already aborted");
  });
});

describe("bidirectional tool calls", () => {
  beforeEach(async () => {
    autoRespondToInitialize();
    await waitForConnect();
  });

  it("oncalltool handles incoming tools/call request", async () => {
    (window as any).MCPApp.oncalltool = (params: any) => {
      return { content: [{ type: "text", text: "result:" + params.name }] };
    };

    hostSends({
      jsonrpc: "2.0",
      id: 999,
      method: "tools/call",
      params: { name: "app-tool", arguments: { x: 1 } },
    });

    await new Promise((r) => setTimeout(r, 10));

    const resp = sentMessages.find(
      (m) => m.id === 999 && m.result
    );
    expect(resp).toBeDefined();
    expect(resp.result.content[0].text).toBe("result:app-tool");
  });

  it("onlisttools handles incoming tools/list request", async () => {
    (window as any).MCPApp.onlisttools = () => {
      return [{ name: "app-tool", description: "An app tool" }];
    };

    hostSends({
      jsonrpc: "2.0",
      id: 888,
      method: "tools/list",
      params: {},
    });

    await new Promise((r) => setTimeout(r, 10));

    const resp = sentMessages.find((m) => m.id === 888 && m.result);
    expect(resp).toBeDefined();
    expect(resp.result.tools[0].name).toBe("app-tool");
  });

  it("returns error when no oncalltool handler set", async () => {
    (window as any).MCPApp.oncalltool = null;

    hostSends({
      jsonrpc: "2.0",
      id: 777,
      method: "tools/call",
      params: { name: "missing" },
    });

    await new Promise((r) => setTimeout(r, 10));

    const resp = sentMessages.find((m) => m.id === 777 && m.error);
    expect(resp).toBeDefined();
    expect(resp.error.message).toContain("No oncalltool handler");
  });

  it("returns empty tools when no onlisttools handler set", async () => {
    (window as any).MCPApp.onlisttools = null;

    hostSends({
      jsonrpc: "2.0",
      id: 666,
      method: "tools/list",
      params: {},
    });

    await new Promise((r) => setTimeout(r, 10));

    const resp = sentMessages.find((m) => m.id === 666 && m.result);
    expect(resp).toBeDefined();
    expect(resp.result.tools).toEqual([]);
  });
});

describe("registerTool API", () => {
  beforeEach(async () => {
    autoRespondToInitialize();
    await waitForConnect();
  });

  it("registerTool registers a tool and auto-handles tools/list", async () => {
    const app = (window as any).MCPApp;
    app.registerTool(
      "my-tool",
      { description: "A test tool" },
      () => ({ content: [{ type: "text", text: "ok" }] })
    );

    hostSends({
      jsonrpc: "2.0",
      id: 1001,
      method: "tools/list",
      params: {},
    });

    await new Promise((r) => setTimeout(r, 10));

    const resp = sentMessages.find((m) => m.id === 1001 && m.result);
    expect(resp).toBeDefined();
    expect(resp.result.tools).toEqual([
      { name: "my-tool", description: "A test tool" },
    ]);
  });

  it("registerTool auto-dispatches tools/call to the right handler", async () => {
    const app = (window as any).MCPApp;
    const handlerA = vi.fn(() => ({ content: [{ type: "text", text: "a" }] }));
    const handlerB = vi.fn(() => ({ content: [{ type: "text", text: "b" }] }));

    app.registerTool("tool-a", { description: "A" }, handlerA);
    app.registerTool("tool-b", { description: "B" }, handlerB);

    hostSends({
      jsonrpc: "2.0",
      id: 1002,
      method: "tools/call",
      params: { name: "tool-b", arguments: { x: 42 } },
    });

    await new Promise((r) => setTimeout(r, 10));

    expect(handlerB).toHaveBeenCalledOnce();
    expect(handlerB).toHaveBeenCalledWith({ x: 42 });
    expect(handlerA).not.toHaveBeenCalled();

    const resp = sentMessages.find((m) => m.id === 1002 && m.result);
    expect(resp).toBeDefined();
    expect(resp.result.content[0].text).toBe("b");
  });

  it("sendToolListChanged sends notification to host", async () => {
    const app = (window as any).MCPApp;
    // Clear sentMessages to isolate our notification.
    sentMessages.length = 0;

    app.sendToolListChanged();

    await new Promise((r) => setTimeout(r, 10));

    const notif = sentMessages.find(
      (m) => m.method === "notifications/tools/list_changed" && m.id == null
    );
    expect(notif).toBeDefined();
  });

  it("registerTool auto-sends toolListChanged on register", async () => {
    const app = (window as any).MCPApp;
    sentMessages.length = 0;

    app.registerTool("auto-notify", {}, () => "ok");

    await new Promise((r) => setTimeout(r, 10));

    const notif = sentMessages.find(
      (m) => m.method === "notifications/tools/list_changed"
    );
    expect(notif).toBeDefined();
  });

  it("tool handle update() changes tool metadata", async () => {
    const app = (window as any).MCPApp;
    const handle = app.registerTool(
      "updatable",
      { description: "old" },
      () => "ok"
    );

    handle.update({ description: "new" });

    hostSends({
      jsonrpc: "2.0",
      id: 1003,
      method: "tools/list",
      params: {},
    });

    await new Promise((r) => setTimeout(r, 10));

    const resp = sentMessages.find((m) => m.id === 1003 && m.result);
    expect(resp).toBeDefined();
    expect(resp.result.tools[0].description).toBe("new");
  });

  it("tool handle disable()/enable() controls visibility", async () => {
    const app = (window as any).MCPApp;
    const handle = app.registerTool(
      "toggleable",
      { description: "toggle" },
      () => "ok"
    );

    handle.disable();

    // tools/list should omit disabled tool.
    hostSends({ jsonrpc: "2.0", id: 1004, method: "tools/list", params: {} });
    await new Promise((r) => setTimeout(r, 10));

    let resp = sentMessages.find((m) => m.id === 1004 && m.result);
    expect(resp).toBeDefined();
    expect(resp.result.tools).toEqual([]);

    handle.enable();

    // tools/list should include it again.
    hostSends({ jsonrpc: "2.0", id: 1005, method: "tools/list", params: {} });
    await new Promise((r) => setTimeout(r, 10));

    resp = sentMessages.find((m) => m.id === 1005 && m.result);
    expect(resp).toBeDefined();
    expect(resp.result.tools.length).toBe(1);
    expect(resp.result.tools[0].name).toBe("toggleable");
  });

  it("tool handle remove() unregisters tool", async () => {
    const app = (window as any).MCPApp;
    const handle = app.registerTool(
      "removable",
      { description: "bye" },
      () => "ok"
    );

    handle.remove();

    // tools/list should return empty.
    hostSends({ jsonrpc: "2.0", id: 1006, method: "tools/list", params: {} });
    await new Promise((r) => setTimeout(r, 10));

    const listResp = sentMessages.find((m) => m.id === 1006 && m.result);
    expect(listResp).toBeDefined();
    expect(listResp.result.tools).toEqual([]);

    // tools/call should return error for removed tool.
    hostSends({
      jsonrpc: "2.0",
      id: 1007,
      method: "tools/call",
      params: { name: "removable" },
    });
    await new Promise((r) => setTimeout(r, 10));

    const callResp = sentMessages.find((m) => m.id === 1007 && m.error);
    expect(callResp).toBeDefined();
  });

  it("tools/call returns error for unknown tool name", async () => {
    const app = (window as any).MCPApp;
    app.registerTool("known", {}, () => "ok");

    hostSends({
      jsonrpc: "2.0",
      id: 1008,
      method: "tools/call",
      params: { name: "unknown-tool", arguments: {} },
    });

    await new Promise((r) => setTimeout(r, 10));

    const resp = sentMessages.find((m) => m.id === 1008 && m.error);
    expect(resp).toBeDefined();
    expect(resp.error.message).toContain("unknown-tool");
  });

  it("includes inputSchema and outputSchema in tools/list", async () => {
    const app = (window as any).MCPApp;
    app.registerTool(
      "schema-tool",
      {
        description: "has schemas",
        inputSchema: { type: "object", properties: { x: { type: "number" } } },
        outputSchema: { type: "object", properties: { y: { type: "string" } } },
      },
      () => ({ y: "hello" })
    );

    hostSends({ jsonrpc: "2.0", id: 1009, method: "tools/list", params: {} });
    await new Promise((r) => setTimeout(r, 10));

    const resp = sentMessages.find((m) => m.id === 1009 && m.result);
    expect(resp).toBeDefined();
    const tool = resp.result.tools[0];
    expect(tool.name).toBe("schema-tool");
    expect(tool.inputSchema).toEqual({
      type: "object",
      properties: { x: { type: "number" } },
    });
    expect(tool.outputSchema).toEqual({
      type: "object",
      properties: { y: { type: "string" } },
    });
  });
});

describe("Standard Schema validation", () => {
  beforeEach(async () => {
    autoRespondToInitialize();
    await waitForConnect();
  });

  it("validates input via ~standard.validate before calling handler", async () => {
    const app = (window as any).MCPApp;
    const handler = vi.fn(() => "ok");

    // Mock Standard Schema v1 object with ~standard property.
    const schema = {
      type: "object",
      "~standard": {
        version: 1,
        vendor: "test",
        validate: (value: any) => {
          if (typeof value.x !== "number") {
            return { issues: [{ message: "x must be a number", path: [{ key: "x" }] }] };
          }
          return { value };
        },
      },
    };

    app.registerTool("validated", { inputSchema: schema }, handler);

    // Valid input — handler should be called.
    hostSends({
      jsonrpc: "2.0",
      id: 2001,
      method: "tools/call",
      params: { name: "validated", arguments: { x: 42 } },
    });
    await new Promise((r) => setTimeout(r, 10));
    expect(handler).toHaveBeenCalledOnce();

    const okResp = sentMessages.find((m: any) => m.id === 2001 && m.result);
    expect(okResp).toBeDefined();
  });

  it("returns error on validation failure", async () => {
    const app = (window as any).MCPApp;
    const handler = vi.fn(() => "ok");

    const schema = {
      "~standard": {
        version: 1,
        vendor: "test",
        validate: (value: any) => {
          if (!value.name) {
            return { issues: [{ message: "name is required", path: [{ key: "name" }] }] };
          }
          return { value };
        },
      },
    };

    app.registerTool("strict-tool", { inputSchema: schema }, handler);

    // Invalid input — handler should NOT be called.
    hostSends({
      jsonrpc: "2.0",
      id: 2002,
      method: "tools/call",
      params: { name: "strict-tool", arguments: {} },
    });
    await new Promise((r) => setTimeout(r, 10));

    expect(handler).not.toHaveBeenCalled();

    const errResp = sentMessages.find((m: any) => m.id === 2002 && m.error);
    expect(errResp).toBeDefined();
    expect(errResp.error.message).toContain("Validation failed");
    expect(errResp.error.message).toContain("name is required");
  });

  it("skips validation when inputSchema has no ~standard property", async () => {
    const app = (window as any).MCPApp;
    const handler = vi.fn(() => "ok");

    // Plain JSON Schema without ~standard — no validation.
    app.registerTool(
      "plain-schema",
      { inputSchema: { type: "object", properties: { x: { type: "number" } } } },
      handler
    );

    hostSends({
      jsonrpc: "2.0",
      id: 2003,
      method: "tools/call",
      params: { name: "plain-schema", arguments: { x: "not-a-number" } },
    });
    await new Promise((r) => setTimeout(r, 10));

    // Handler called regardless — no validation without ~standard.
    expect(handler).toHaveBeenCalledOnce();
  });
});

describe("pre-handshake guarding", () => {
  it("callTool rejects when not connected", async () => {
    expect((window as any).MCPApp.connected).toBe(false);
    await expect(
      (window as any).MCPApp.callTool("test", {})
    ).rejects.toThrow("Not connected");
  });

  it("readResource rejects when not connected", async () => {
    await expect(
      (window as any).MCPApp.readResource("ui://test")
    ).rejects.toThrow("Not connected");
  });

  it("sendToolListChanged is silently dropped before handshake", () => {
    const app = (window as any).MCPApp;
    sentMessages.length = 0;

    app.sendToolListChanged();

    const notif = sentMessages.find(
      (m: any) => m.method === "notifications/tools/list_changed"
    );
    expect(notif).toBeUndefined();
  });

  it("log is silently dropped before handshake", () => {
    const app = (window as any).MCPApp;
    sentMessages.length = 0;

    app.log("info", "test");

    const logMsg = sentMessages.find((m: any) => m.method === "ui/log");
    expect(logMsg).toBeUndefined();
  });
});

describe("idempotency", () => {
  it("does not double-register on second script load", () => {
    const firstApp = (window as any).MCPApp;
    loadBridge(); // Load again.
    expect((window as any).MCPApp).toBe(firstApp);
  });
});

// ---------------------------------------------------------------------------
// SEP-2356 Phase 2.1 — file picker primitives
// ---------------------------------------------------------------------------
//
// These tests exercise mcp.selectFile() / mcp.selectFiles() — the bridge's
// in-iframe DOM file picker (Option B per issue #358). The bridge synthesizes
// a hidden `<input type="file">` element, awaits the user's selection (or
// cancel), runs descriptor validation (size + MIME), and returns an RFC 2397
// base64 data URI matching `core.EncodeDataURI` byte-for-byte.
//
// Test kit overview
// -----------------
// `setupFilePicker(opts)` patches document.createElement('input') to return
// a synthesizable input, replaces FileReader with a deterministic stub, and
// returns a `pick(files)` callable that simulates the user selecting files.
// Calling `pick(null)` simulates cancellation.
//
// Why: real browsers gate <input>.click() to user-gesture handlers + open a
// native dialog. JSDom does neither. The kit lets tests exercise the
// post-selection logic (validation, FileReader, percent-encoding) without
// needing to drive a real file chooser.

interface FilePickerKit {
  /** Resolve the pending file picker with these files (null = cancel). */
  pick: (files: File[] | null) => void;
  /** Most recent input element synthesized by the bridge. */
  lastInput: () => HTMLInputElement;
  /** Restore original document.createElement / FileReader. */
  restore: () => void;
}

function setupFilePicker(): FilePickerKit {
  const inputs: HTMLInputElement[] = [];
  let pendingResolve: ((files: File[] | null) => void) | null = null;

  const realCreateElement = document.createElement.bind(document);
  vi.spyOn(document, "createElement").mockImplementation(
    function (this: Document, tag: string, options?: ElementCreationOptions) {
      const el = realCreateElement(tag, options);
      if (tag.toLowerCase() === "input") {
        inputs.push(el as HTMLInputElement);
        // Override .click() so calling it parks until pick(...) fires.
        (el as any).click = () => {
          // The bridge attaches change/cancel listeners before calling click().
          // We capture a resolver that the test driver invokes via pick().
          pendingResolve = (files) => {
            if (files === null) {
              el.dispatchEvent(new Event("cancel"));
            } else {
              Object.defineProperty(el, "files", {
                value: makeFileList(files),
                configurable: true,
              });
              el.dispatchEvent(new Event("change"));
            }
          };
        };
      }
      return el;
    } as typeof document.createElement
  );

  // Deterministic FileReader stub — readAsDataURL produces
  // "data:<type>;base64,<payload>" without a name= parameter (just like a real
  // browser). The bridge is responsible for injecting name= itself.
  class StubFileReader {
    public result: string | null = null;
    public error: Error | null = null;
    public onload: (() => void) | null = null;
    public onerror: (() => void) | null = null;
    readAsDataURL(file: File) {
      // Read the underlying bytes synchronously via the bridge-supplied File
      // (we use a custom File constructor that exposes _bytes for this).
      const bytes: Uint8Array = (file as any)._bytes ?? new Uint8Array();
      const base64 = btoa(String.fromCharCode(...bytes));
      this.result = `data:${file.type};base64,${base64}`;
      // Fire onload async to mirror real FileReader.
      setTimeout(() => this.onload?.(), 0);
    }
  }
  (globalThis as any).FileReader = StubFileReader;

  return {
    pick: (files) => {
      if (!pendingResolve) {
        throw new Error("selectFile() was not awaiting — pick() called too early");
      }
      const fn = pendingResolve;
      pendingResolve = null;
      fn(files);
    },
    lastInput: () => inputs[inputs.length - 1],
    restore: () => {
      vi.restoreAllMocks();
      delete (globalThis as any).FileReader;
    },
  };
}

/** Construct a File-like with attached raw bytes for the StubFileReader. */
function makeFile(name: string, type: string, bytes: Uint8Array): File {
  const file = new File([bytes], name, { type });
  (file as any)._bytes = bytes;
  return file;
}

function makeFileList(files: File[]): FileList {
  const list = files as unknown as FileList & File[];
  Object.defineProperty(list, "item", {
    value: (i: number) => files[i] ?? null,
    configurable: true,
  });
  return list;
}

/** Decode the base64 payload from a data URI back to bytes. */
function decodeDataURIBytes(uri: string): Uint8Array {
  const comma = uri.indexOf(",");
  const payload = uri.slice(comma + 1);
  const binary = atob(payload);
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
  return out;
}

describe("selectFile / selectFiles (SEP-2356 Phase 2.1)", () => {
  let kit: FilePickerKit;

  beforeEach(() => {
    kit = setupFilePicker();
  });

  afterEach(() => {
    kit.restore();
  });

  // verifies: the public surface advertises both single + multi entry points
  // alongside the existing host-bound primitives.
  it("exposes selectFile and selectFiles on MCPApp", () => {
    const app = (window as any).MCPApp;
    expect(typeof app.selectFile).toBe("function");
    expect(typeof app.selectFiles).toBe("function");
  });

  // verifies: a successful selection resolves with an RFC 2397 base64 data URI
  // carrying the file's media type. Critical happy-path round-trip.
  it("selectFile resolves with a data URI on selection", async () => {
    const app = (window as any).MCPApp;
    const promise = app.selectFile();
    const file = makeFile("hello.bin", "application/octet-stream", new Uint8Array([1, 2, 3]));
    setTimeout(() => kit.pick([file]), 0);
    const result = await promise;
    expect(result).toMatch(/^data:application\/octet-stream;name=hello\.bin;base64,/);
  });

  // verifies: filenames with characters outside the unreserved set get
  // percent-encoded so the wire shape matches `core.EncodeDataURI` exactly
  // (Go side encodes parens as %28/%29 via url.PathEscape; we must match).
  it("name= parameter percent-encodes special characters to match core.EncodeDataURI", async () => {
    const app = (window as any).MCPApp;
    const promise = app.selectFile();
    const file = makeFile("my photo (1).png", "image/png", new Uint8Array([0]));
    setTimeout(() => kit.pick([file]), 0);
    const result = await promise;
    expect(result).toContain(";name=my%20photo%20%281%29.png;");
  });

  // verifies: `accept` from the descriptor reaches the synthesized <input>'s
  // accept attribute so the native picker pre-filters the file list. This is
  // a hint — full enforcement runs post-selection (test below).
  it("accept patterns are propagated to the input element", async () => {
    const app = (window as any).MCPApp;
    const promise = app.selectFile({ accept: ["image/*", ".pdf"] });
    setTimeout(() => {
      expect(kit.lastInput().accept).toBe("image/*,.pdf");
      kit.pick([makeFile("x.png", "image/png", new Uint8Array([0]))]);
    }, 0);
    await promise;
  });

  // verifies: oversized files are rejected with MCPFileTooLarge BEFORE the
  // FileReader runs — saves a wasted decode and matches server-side
  // ValidateFileInput semantics (issue #361).
  it("oversized file rejects with MCPFileTooLarge", async () => {
    const app = (window as any).MCPApp;
    const promise = app.selectFile({ maxSize: 4 });
    const big = makeFile("big.bin", "application/octet-stream", new Uint8Array(8));
    setTimeout(() => kit.pick([big]), 0);
    await expect(promise).rejects.toMatchObject({ name: "MCPFileTooLarge" });
  });

  // verifies: MIME mismatch rejects with MCPFileTypeNotAccepted. Mirrors the
  // server-side -32602 `file_type_not_accepted` reason from #361.
  it("wrong MIME rejects with MCPFileTypeNotAccepted", async () => {
    const app = (window as any).MCPApp;
    const promise = app.selectFile({ accept: ["image/*"] });
    const pdf = makeFile("doc.pdf", "application/pdf", new Uint8Array([0]));
    setTimeout(() => kit.pick([pdf]), 0);
    await expect(promise).rejects.toMatchObject({ name: "MCPFileTypeNotAccepted" });
  });

  // verifies: wildcard subtype matching ("image/*" accepts every image/...)
  // matches the server-side validator's pattern rules.
  it("wildcard subtype matches a concrete MIME", async () => {
    const app = (window as any).MCPApp;
    const promise = app.selectFile({ accept: ["image/*"] });
    const png = makeFile("x.png", "image/png", new Uint8Array([0]));
    setTimeout(() => kit.pick([png]), 0);
    await expect(promise).resolves.toMatch(/^data:image\/png;/);
  });

  // verifies: extension-only accept patterns (".pdf") match by filename
  // suffix, regardless of declared media type.
  it("extension hint matches by filename suffix", async () => {
    const app = (window as any).MCPApp;
    const promise = app.selectFile({ accept: [".pdf"] });
    const pdf = makeFile("doc.PDF", "application/pdf", new Uint8Array([0]));
    setTimeout(() => kit.pick([pdf]), 0);
    await expect(promise).resolves.toMatch(/^data:application\/pdf;/);
  });

  // verifies: an empty descriptor accepts any file (no constraints) — the
  // "process_any_file" tool on the server side mirrors this.
  it("empty descriptor accepts anything", async () => {
    const app = (window as any).MCPApp;
    const promise = app.selectFile();
    const txt = makeFile("notes.txt", "text/plain", new Uint8Array([97, 98, 99]));
    setTimeout(() => kit.pick([txt]), 0);
    await expect(promise).resolves.toMatch(/^data:text\/plain;/);
  });

  // verifies: cancellation surfaces a typed error rather than hanging the
  // Promise. Caller code can branch on the error name without parsing strings.
  it("cancellation rejects with MCPFileSelectionCanceled", async () => {
    const app = (window as any).MCPApp;
    const promise = app.selectFile();
    setTimeout(() => kit.pick(null), 0);
    await expect(promise).rejects.toMatchObject({ name: "MCPFileSelectionCanceled" });
  });

  // verifies: selectFiles returns an array preserving selection order, and
  // the synthesized input has the `multiple` attribute set so the native
  // picker allows multi-select.
  it("selectFiles returns an ordered array of data URIs", async () => {
    const app = (window as any).MCPApp;
    const promise = app.selectFiles({ accept: ["application/pdf"] });
    const a = makeFile("a.pdf", "application/pdf", new Uint8Array([0]));
    const b = makeFile("b.pdf", "application/pdf", new Uint8Array([1]));
    setTimeout(() => {
      expect(kit.lastInput().multiple).toBe(true);
      kit.pick([a, b]);
    }, 0);
    const result = await promise;
    expect(Array.isArray(result)).toBe(true);
    expect(result).toHaveLength(2);
    expect(result[0]).toContain(";name=a.pdf;");
    expect(result[1]).toContain(";name=b.pdf;");
  });

  // verifies: arbitrary binary bytes survive the round-trip — useful guard
  // against accidental UTF-8 normalization or ASCII-only encoding paths.
  it("binary bytes round-trip through base64", async () => {
    const app = (window as any).MCPApp;
    const promise = app.selectFile();
    const bytes = new Uint8Array([0x00, 0x01, 0xff, 0xfe, 0x42]);
    setTimeout(() => kit.pick([makeFile("x.bin", "application/octet-stream", bytes)]), 0);
    const uri = await promise;
    expect(decodeDataURIBytes(uri)).toEqual(bytes);
  });

  // verifies: the bridge produces the canonical Go-side string for a known
  // input. If `core.EncodeDataURI` ever changes shape (separator order,
  // encoding rules), this freezes the divergence and forces explicit
  // resync. Frozen against core/datauri_test.go expectations.
  it("canonical interop with core.EncodeDataURI for a known input", async () => {
    const app = (window as any).MCPApp;
    const promise = app.selectFile();
    const file = makeFile("report.txt", "text/plain", new Uint8Array([104, 101, 108, 108, 111]));
    setTimeout(() => kit.pick([file]), 0);
    const result = await promise;
    expect(result).toBe("data:text/plain;name=report.txt;base64,aGVsbG8=");
  });
});
