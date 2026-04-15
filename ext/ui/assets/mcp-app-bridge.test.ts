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

  it("openLink sends notification (no response expected)", async () => {
    (window as any).MCPApp.openLink("https://example.com");

    await new Promise((r) => setTimeout(r, 10));
    const msg = sentMessages.find((m) => m.method === "ui/open-link");
    expect(msg).toBeDefined();
    expect(msg.params.url).toBe("https://example.com");
    expect(msg.id).toBeUndefined();
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

describe("graceful degradation", () => {
  it("callTool rejects when not connected", async () => {
    expect((window as any).MCPApp.connected).toBe(false);
    await expect(
      (window as any).MCPApp.callTool("test", {})
    ).rejects.toThrow("Not connected");
  });
});

describe("idempotency", () => {
  it("does not double-register on second script load", () => {
    const firstApp = (window as any).MCPApp;
    loadBridge(); // Load again.
    expect((window as any).MCPApp).toBe(firstApp);
  });
});
