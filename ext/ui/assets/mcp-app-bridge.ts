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
  styles?: {
    variables?: Record<string, string>;
    css?: { fonts?: string };
  };
  safeAreaInsets?: {
    top: number;
    right: number;
    bottom: number;
    left: number;
  };
  [key: string]: unknown;
}

/** Options for request methods (callTool, readResource, etc.). */
interface RequestOptions {
  /** AbortSignal for cancellation. */
  signal?: AbortSignal;
  /** Timeout in milliseconds (shorthand for AbortSignal.timeout). */
  timeout?: number;
}

/** Handler for incoming tool calls from the host. */
type CallToolHandler = (params: {
  name: string;
  arguments: Record<string, unknown>;
}) => unknown | Promise<unknown>;

/** Handler for incoming list-tools requests from the host. */
type ListToolsHandler = () =>
  | Array<{ name: string; description?: string; inputSchema?: unknown }>
  | Promise<Array<{ name: string; description?: string; inputSchema?: unknown }>>;

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

  // Configuration: set window.MCPAppConfig before loading this script
  // to customize app identity and protocol version. See BridgeTemplateDef()
  // in ext/ui for the Go template that renders config + bridge together.
  const config = (window as any).MCPAppConfig || {};
  const APP_NAME: string = config.name || "mcp-app";
  const APP_VERSION: string = config.version || "0.0.0";
  const PROTOCOL_VERSION: string = config.protocolVersion || "2026-01-26";

  let nextId = 1;
  const pending = new Map<
    number,
    { resolve: (v: unknown) => void; reject: (e: Error) => void }
  >();
  const listeners = new Map<string, Set<(data: any) => void>>();

  let _connected = false;
  let _hostContext: HostContext | null = null;
  let _hostCapabilities: Record<string, unknown> | null = null;

  // Bidirectional handlers (host → app requests).
  let _oncalltool: CallToolHandler | null = null;
  let _onlisttools: ListToolsHandler | null = null;

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

  function request(
    method: string,
    params?: unknown,
    options?: RequestOptions
  ): Promise<unknown> {
    if (!_connected && method !== "ui/initialize") {
      return Promise.reject(new Error("Not connected to MCP host"));
    }

    // Resolve signal: explicit signal > timeout shorthand > default 30s.
    let signal = options?.signal;
    if (!signal && options?.timeout) {
      signal = AbortSignal.timeout(options.timeout);
    }

    return new Promise((resolve, reject) => {
      const id = nextId++;
      const cleanup = () => { pending.delete(id); };

      pending.set(id, { resolve, reject });
      send({ jsonrpc: "2.0", id, method, params: params || {} });

      // AbortSignal-based cancellation.
      if (signal) {
        if (signal.aborted) {
          cleanup();
          reject(new Error(signal.reason?.message || "Aborted"));
          return;
        }
        signal.addEventListener("abort", () => {
          if (pending.has(id)) {
            cleanup();
            reject(new Error(signal!.reason?.message || "Aborted: " + method));
          }
        }, { once: true });
      } else {
        // Default 30s timeout when no signal provided.
        setTimeout(() => {
          if (pending.has(id)) {
            cleanup();
            reject(new Error("Request timeout: " + method));
          }
        }, 30000);
      }
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

    // Request from host (has both id and method) — bidirectional call.
    const req = msg as JsonRpcRequest;
    if (req.id != null && req.method) {
      handleHostRequest(req);
      return;
    }

    // Notification from host (no id, only method).
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
        applyHostStyles(_hostContext!);
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

  // --- Style utilities -----------------------------------------------------

  function applyTheme(theme: string): void {
    const root = document.documentElement;
    root.setAttribute("data-theme", theme);
    root.style.colorScheme = theme;
  }

  function applyStyleVariables(
    variables: Record<string, string>,
    root: HTMLElement = document.documentElement
  ): void {
    for (const [key, value] of Object.entries(variables)) {
      if (value !== undefined) {
        root.style.setProperty(key, value);
      }
    }
  }

  const FONTS_STYLE_ID = "__mcp-host-fonts";

  function applyFonts(fontCss: string): void {
    if (document.getElementById(FONTS_STYLE_ID)) return;
    const style = document.createElement("style");
    style.id = FONTS_STYLE_ID;
    style.textContent = fontCss;
    document.head.appendChild(style);
  }

  /** Apply all available styles from the host context. */
  function applyHostStyles(ctx: HostContext): void {
    if (ctx.theme) applyTheme(ctx.theme);
    if (ctx.styles?.variables) applyStyleVariables(ctx.styles.variables);
    if (ctx.styles?.css?.fonts) applyFonts(ctx.styles.css.fonts);
  }

  // --- Bidirectional handlers (host → app requests) -----------------------

  /** Respond to a JSON-RPC request from the host. */
  function respond(id: number, result: unknown, error?: unknown): void {
    if (error) {
      send({
        jsonrpc: "2.0",
        id,
        error: {
          code: -32000,
          message: String(error),
        },
      } as any);
    } else {
      send({ jsonrpc: "2.0", id, result } as any);
    }
  }

  async function handleHostRequest(req: JsonRpcRequest): Promise<void> {
    if (req.id == null) return; // Not a request, just a notification.

    const id = req.id;
    const params = (req.params || {}) as any;

    try {
      switch (req.method) {
        case "tools/call": {
          if (_oncalltool) {
            const result = await _oncalltool({
              name: params.name || "",
              arguments: params.arguments || {},
            });
            respond(id, result);
          } else {
            respond(id, null, "No oncalltool handler registered");
          }
          break;
        }
        case "tools/list": {
          if (_onlisttools) {
            const tools = await _onlisttools();
            respond(id, { tools });
          } else {
            respond(id, { tools: [] });
          }
          break;
        }
        default:
          respond(id, null, "Method not found: " + req.method);
          break;
      }
    } catch (e) {
      respond(id, null, e instanceof Error ? e.message : String(e));
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

    request("ui/initialize", {
      protocolVersion: PROTOCOL_VERSION,
      appInfo: { name: APP_NAME, version: APP_VERSION },
      appCapabilities: {},
    })
      .then((result: any) => {
        clearTimeout(timeout);
        _connected = true;
        _hostContext = result?.hostContext || result || {};
        _hostCapabilities = result?.capabilities || {};
        // Auto-apply host styles on connect.
        applyHostStyles(_hostContext!);
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

    // Host-bound methods (all support optional RequestOptions).
    callTool(
      name: string,
      args?: Record<string, unknown>,
      options?: RequestOptions
    ): Promise<ToolCallResult> {
      return request("tools/call", {
        name,
        arguments: args || {},
      }, options) as Promise<ToolCallResult>;
    },

    readResource(uri: string, options?: RequestOptions): Promise<ResourceReadResult> {
      return request("resources/read", { uri }, options) as Promise<ResourceReadResult>;
    },

    sendMessage(message: unknown, options?: RequestOptions): Promise<unknown> {
      return request("ui/message", message, options);
    },

    updateModelContext(context: unknown, options?: RequestOptions): Promise<unknown> {
      return request("ui/update-model-context", { context }, options);
    },

    openLink(url: string, options?: RequestOptions): Promise<unknown> {
      return request("ui/open-link", { url }, options);
    },

    downloadFile(url: string, filename?: string, options?: RequestOptions): Promise<unknown> {
      return request("ui/download-file", { url, filename }, options);
    },

    requestDisplayMode(mode: string, options?: RequestOptions): Promise<unknown> {
      return request("ui/request-display-mode", { mode }, options);
    },

    requestTeardown(): void {
      notify("ui/teardown", {});
    },

    log(level: string, message: string, data?: unknown): void {
      notify("ui/log", { level, message, data });
    },

    // Style utilities.
    applyTheme,
    applyStyleVariables,
    applyFonts,
    applyHostStyles,

    // Bidirectional handlers (set these before connecting).
    set oncalltool(handler: CallToolHandler | null) { _oncalltool = handler; },
    get oncalltool(): CallToolHandler | null { return _oncalltool; },
    set onlisttools(handler: ListToolsHandler | null) { _onlisttools = handler; },
    get onlisttools(): ListToolsHandler | null { return _onlisttools; },

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
