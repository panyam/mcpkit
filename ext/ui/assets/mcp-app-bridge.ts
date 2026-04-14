/**
 * MCP App Bridge — framework-agnostic postMessage transport for MCP Apps.
 *
 * This script handles the iframe-side JSON-RPC 2.0 protocol between an
 * MCP App (HTML rendered in a sandboxed iframe) and the host (Claude,
 * ChatGPT, VS Code Copilot, MCPJam, etc.).
 *
 * Usage: include via <script> tag in server-generated HTML. The bridge
 * self-initializes on load and exposes a global `MCPApp` singleton.
 *
 * @see https://github.com/panyam/mcpkit
 */

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Event names emitted by the bridge. */
type MCPAppEvent =
  | "connected"
  | "toolinput"
  | "toolinputpartial"
  | "toolresult"
  | "toolcancelled"
  | "hostcontextchanged"
  | "teardown";

/** Payload delivered with each event. */
interface MCPAppEventMap {
  connected: { hostContext: HostContext; capabilities: Record<string, unknown> };
  toolinput: { tool: string; arguments: Record<string, unknown> };
  toolinputpartial: { tool: string; arguments: Record<string, unknown> };
  toolresult: { tool: string; result: unknown };
  toolcancelled: { tool: string };
  hostcontextchanged: { hostContext: HostContext };
  teardown: Record<string, never>;
}

interface HostContext {
  theme?: string;
  locale?: string;
  dimensions?: { width: number; height: number };
  [key: string]: unknown;
}

interface ToolCallResult {
  content?: Array<{ type: string; text?: string; [k: string]: unknown }>;
  isError?: boolean;
  [key: string]: unknown;
}

interface ResourceReadResult {
  contents?: Array<{
    uri: string;
    mimeType?: string;
    text?: string;
    blob?: string;
    [k: string]: unknown;
  }>;
  [key: string]: unknown;
}

// ---------------------------------------------------------------------------
// JSON-RPC helpers
// ---------------------------------------------------------------------------

interface JsonRpcRequest {
  jsonrpc: "2.0";
  id?: number;
  method: string;
  params?: unknown;
}

interface JsonRpcResponse {
  jsonrpc: "2.0";
  id: number;
  result?: unknown;
  error?: { code: number; message: string; data?: unknown };
}

type JsonRpcMessage = JsonRpcRequest | JsonRpcResponse;

// ---------------------------------------------------------------------------
// Bridge implementation
// ---------------------------------------------------------------------------

(function () {
  "use strict";

  // Guard against double-inclusion.
  if ((window as any).MCPApp) return;

  let nextId = 1;
  const pending = new Map<
    number,
    { resolve: (v: unknown) => void; reject: (e: Error) => void }
  >();
  const listeners = new Map<string, Set<(data: any) => void>>();

  let _connected = false;
  let _hostContext: HostContext | null = null;
  let _hostCapabilities: Record<string, unknown> | null = null;

  // --- Event emitter -------------------------------------------------------

  function on<E extends MCPAppEvent>(
    event: E,
    handler: (data: MCPAppEventMap[E]) => void
  ): () => void {
    let set = listeners.get(event);
    if (!set) {
      set = new Set();
      listeners.set(event, set);
    }
    set.add(handler);
    return () => set!.delete(handler);
  }

  function off<E extends MCPAppEvent>(
    event: E,
    handler: (data: MCPAppEventMap[E]) => void
  ): void {
    listeners.get(event)?.delete(handler);
  }

  function once<E extends MCPAppEvent>(
    event: E,
    handler: (data: MCPAppEventMap[E]) => void
  ): () => void {
    const unsub = on(event, function wrapper(data: MCPAppEventMap[E]) {
      unsub();
      handler(data);
    });
    return unsub;
  }

  function emit(event: string, data: unknown): void {
    const set = listeners.get(event);
    if (set) {
      set.forEach((fn: (data: any) => void) => {
        try {
          fn(data);
        } catch (e) {
          console.error("[MCPApp] handler error for " + event + ":", e);
        }
      });
    }
    // Also dispatch a CustomEvent on document for HTMX / declarative listeners.
    try {
      document.dispatchEvent(
        new CustomEvent("mcp:" + event, { detail: data })
      );
    } catch (_) {
      // CustomEvent may not be available in very old environments.
    }
  }

  // --- postMessage transport -----------------------------------------------

  function send(msg: JsonRpcMessage): void {
    if (window.parent && window.parent !== window) {
      window.parent.postMessage(msg, "*");
    }
  }

  function request(method: string, params?: unknown): Promise<unknown> {
    if (!_connected && method !== "ui/initialize") {
      return Promise.reject(new Error("Not connected to MCP host"));
    }
    return new Promise((resolve, reject) => {
      const id = nextId++;
      pending.set(id, { resolve, reject });
      send({ jsonrpc: "2.0", id, method, params: params || {} });
      // Timeout after 30 seconds.
      setTimeout(() => {
        if (pending.has(id)) {
          pending.delete(id);
          reject(new Error("Request timeout: " + method));
        }
      }, 30000);
    });
  }

  function notify(method: string, params?: unknown): void {
    send({ jsonrpc: "2.0", method, params: params || {} });
  }

  // --- Incoming message handler --------------------------------------------

  function isJsonRpc(data: unknown): data is JsonRpcMessage {
    return (
      typeof data === "object" &&
      data !== null &&
      (data as any).jsonrpc === "2.0"
    );
  }

  function handleMessage(event: MessageEvent): void {
    const msg = event.data;
    if (!isJsonRpc(msg)) return;

    // Response to one of our requests.
    if ("id" in msg && msg.id != null && !("method" in msg)) {
      const resp = msg as JsonRpcResponse;
      const p = pending.get(resp.id);
      if (p) {
        pending.delete(resp.id);
        if (resp.error) {
          p.reject(new Error(resp.error.message));
        } else {
          p.resolve(resp.result);
        }
      }
      return;
    }

    // Notification or request from host.
    const req = msg as JsonRpcRequest;
    switch (req.method) {
      case "ui/notifications/tool-input": {
        const p = (req.params || {}) as any;
        emit("toolinput", { tool: p.name || "", arguments: p.arguments || {} });
        break;
      }
      case "ui/notifications/tool-input-partial": {
        const p = (req.params || {}) as any;
        emit("toolinputpartial", {
          tool: p.name || "",
          arguments: p.arguments || {},
        });
        break;
      }
      case "ui/notifications/tool-result": {
        const p = (req.params || {}) as any;
        emit("toolresult", { tool: p.name || "", result: p });
        break;
      }
      case "ui/notifications/tool-cancelled": {
        const p = (req.params || {}) as any;
        emit("toolcancelled", { tool: p.name || "" });
        break;
      }
      case "ui/notifications/host-context-changed": {
        const p = (req.params || {}) as any;
        _hostContext = p.hostContext || p;
        emit("hostcontextchanged", { hostContext: _hostContext! });
        break;
      }
      case "ui/resource-teardown": {
        emit("teardown", {});
        break;
      }
      default:
        // Unknown method — ignore.
        break;
    }
  }

  window.addEventListener("message", handleMessage);

  // --- ResizeObserver (auto-size reporting) ---------------------------------

  let resizeRaf = 0;

  function setupResizeObserver(): void {
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => {
      if (resizeRaf) cancelAnimationFrame(resizeRaf);
      resizeRaf = requestAnimationFrame(() => {
        const body = document.body;
        if (!body) return;
        notify("ui/notifications/size-changed", {
          width: body.scrollWidth,
          height: body.scrollHeight,
        });
      });
    });
    if (document.body) {
      observer.observe(document.body);
    } else {
      document.addEventListener("DOMContentLoaded", () => {
        if (document.body) observer.observe(document.body);
      });
    }
  }

  // --- Initialize handshake ------------------------------------------------

  function initialize(): void {
    // Only attempt if we're inside an iframe.
    if (window.parent === window) return;

    const timeout = setTimeout(() => {
      // No response — not inside an MCP Apps host.
      _connected = false;
    }, 2000);

    request("ui/initialize", {})
      .then((result: any) => {
        clearTimeout(timeout);
        _connected = true;
        _hostContext = result?.hostContext || result || {};
        _hostCapabilities = result?.capabilities || {};
        emit("connected", {
          hostContext: _hostContext!,
          capabilities: _hostCapabilities!,
        });
        // Signal that we're ready.
        notify("ui/notifications/initialized", { initialized: true });
        setupResizeObserver();
      })
      .catch(() => {
        clearTimeout(timeout);
        _connected = false;
      });
  }

  // --- Public API ----------------------------------------------------------

  const MCPApp = {
    // State (read-only from consumer perspective).
    get connected(): boolean {
      return _connected;
    },
    get hostContext(): HostContext | null {
      return _hostContext;
    },
    get hostCapabilities(): Record<string, unknown> | null {
      return _hostCapabilities;
    },

    // Event registration.
    on,
    off,
    once,

    // Host-bound methods.
    callTool(
      name: string,
      args?: Record<string, unknown>
    ): Promise<ToolCallResult> {
      return request("tools/call", {
        name,
        arguments: args || {},
      }) as Promise<ToolCallResult>;
    },

    readResource(uri: string): Promise<ResourceReadResult> {
      return request("resources/read", { uri }) as Promise<ResourceReadResult>;
    },

    sendMessage(message: unknown): Promise<unknown> {
      return request("ui/message", { message });
    },

    updateModelContext(context: unknown): Promise<unknown> {
      return request("ui/update-model-context", { context });
    },

    openLink(url: string): void {
      notify("ui/open-link", { url });
    },

    downloadFile(url: string, filename?: string): void {
      notify("ui/download-file", { url, filename });
    },

    requestDisplayMode(mode: string): Promise<unknown> {
      return request("ui/request-display-mode", { mode });
    },

    requestTeardown(): void {
      notify("ui/teardown", {});
    },

    log(level: string, message: string, data?: unknown): void {
      notify("ui/log", { level, message, data });
    },

    // Utility.
    isHosted(): boolean {
      return _connected;
    },
  };

  (window as any).MCPApp = MCPApp;

  // Kick off the handshake.
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", initialize);
  } else {
    initialize();
  }
})();
