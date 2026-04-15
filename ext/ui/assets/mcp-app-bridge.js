"use strict";
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
// Bridge implementation
// ---------------------------------------------------------------------------
(function () {
    "use strict";
    // Guard against double-inclusion.
    if (window.MCPApp)
        return;
    // Configuration: set window.MCPAppConfig before loading this script
    // to customize app identity and protocol version. See BridgeTemplateDef()
    // in ext/ui for the Go template that renders config + bridge together.
    const config = window.MCPAppConfig || {};
    const APP_NAME = config.name || "mcp-app";
    const APP_VERSION = config.version || "0.0.0";
    const PROTOCOL_VERSION = config.protocolVersion || "2026-01-26";
    let nextId = 1;
    const pending = new Map();
    const listeners = new Map();
    let _connected = false;
    let _hostContext = null;
    let _hostCapabilities = null;
    // --- Event emitter -------------------------------------------------------
    function on(event, handler) {
        let set = listeners.get(event);
        if (!set) {
            set = new Set();
            listeners.set(event, set);
        }
        set.add(handler);
        return () => set.delete(handler);
    }
    function off(event, handler) {
        var _a;
        (_a = listeners.get(event)) === null || _a === void 0 ? void 0 : _a.delete(handler);
    }
    function once(event, handler) {
        const unsub = on(event, function wrapper(data) {
            unsub();
            handler(data);
        });
        return unsub;
    }
    function emit(event, data) {
        const set = listeners.get(event);
        if (set) {
            set.forEach((fn) => {
                try {
                    fn(data);
                }
                catch (e) {
                    console.error("[MCPApp] handler error for " + event + ":", e);
                }
            });
        }
        // Also dispatch a CustomEvent on document for HTMX / declarative listeners.
        try {
            document.dispatchEvent(new CustomEvent("mcp:" + event, { detail: data }));
        }
        catch (_) {
            // CustomEvent may not be available in very old environments.
        }
    }
    // --- postMessage transport -----------------------------------------------
    function send(msg) {
        if (window.parent && window.parent !== window) {
            window.parent.postMessage(msg, "*");
        }
    }
    function request(method, params) {
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
    function notify(method, params) {
        send({ jsonrpc: "2.0", method, params: params || {} });
    }
    // --- Incoming message handler --------------------------------------------
    function isJsonRpc(data) {
        return (typeof data === "object" &&
            data !== null &&
            data.jsonrpc === "2.0");
    }
    function handleMessage(event) {
        const msg = event.data;
        if (!isJsonRpc(msg))
            return;
        // Response to one of our requests.
        if ("id" in msg && msg.id != null && !("method" in msg)) {
            const resp = msg;
            const p = pending.get(resp.id);
            if (p) {
                pending.delete(resp.id);
                if (resp.error) {
                    p.reject(new Error(resp.error.message));
                }
                else {
                    p.resolve(resp.result);
                }
            }
            return;
        }
        // Notification or request from host.
        const req = msg;
        switch (req.method) {
            case "ui/notifications/tool-input": {
                const p = (req.params || {});
                emit("toolinput", { tool: p.name || "", arguments: p.arguments || {} });
                break;
            }
            case "ui/notifications/tool-input-partial": {
                const p = (req.params || {});
                emit("toolinputpartial", {
                    tool: p.name || "",
                    arguments: p.arguments || {},
                });
                break;
            }
            case "ui/notifications/tool-result": {
                const p = (req.params || {});
                emit("toolresult", { tool: p.name || "", result: p });
                break;
            }
            case "ui/notifications/tool-cancelled": {
                const p = (req.params || {});
                emit("toolcancelled", { tool: p.name || "" });
                break;
            }
            case "ui/notifications/host-context-changed": {
                const p = (req.params || {});
                _hostContext = p.hostContext || p;
                emit("hostcontextchanged", { hostContext: _hostContext });
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
    function setupResizeObserver() {
        if (typeof ResizeObserver === "undefined")
            return;
        const observer = new ResizeObserver(() => {
            if (resizeRaf)
                cancelAnimationFrame(resizeRaf);
            resizeRaf = requestAnimationFrame(() => {
                const body = document.body;
                if (!body)
                    return;
                notify("ui/notifications/size-changed", {
                    width: body.scrollWidth,
                    height: body.scrollHeight,
                });
            });
        });
        if (document.body) {
            observer.observe(document.body);
        }
        else {
            document.addEventListener("DOMContentLoaded", () => {
                if (document.body)
                    observer.observe(document.body);
            });
        }
    }
    // --- Initialize handshake ------------------------------------------------
    function initialize() {
        // Only attempt if we're inside an iframe.
        if (window.parent === window)
            return;
        const timeout = setTimeout(() => {
            // No response — not inside an MCP Apps host.
            _connected = false;
        }, 2000);
        request("ui/initialize", {
            protocolVersion: PROTOCOL_VERSION,
            appInfo: { name: APP_NAME, version: APP_VERSION },
            appCapabilities: {},
        })
            .then((result) => {
            clearTimeout(timeout);
            _connected = true;
            _hostContext = (result === null || result === void 0 ? void 0 : result.hostContext) || result || {};
            _hostCapabilities = (result === null || result === void 0 ? void 0 : result.capabilities) || {};
            emit("connected", {
                hostContext: _hostContext,
                capabilities: _hostCapabilities,
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
        get connected() {
            return _connected;
        },
        get hostContext() {
            return _hostContext;
        },
        get hostCapabilities() {
            return _hostCapabilities;
        },
        // Event registration.
        on,
        off,
        once,
        // Host-bound methods.
        callTool(name, args) {
            return request("tools/call", {
                name,
                arguments: args || {},
            });
        },
        readResource(uri) {
            return request("resources/read", { uri });
        },
        sendMessage(message) {
            return request("ui/message", { message });
        },
        updateModelContext(context) {
            return request("ui/update-model-context", { context });
        },
        openLink(url) {
            notify("ui/open-link", { url });
        },
        downloadFile(url, filename) {
            notify("ui/download-file", { url, filename });
        },
        requestDisplayMode(mode) {
            return request("ui/request-display-mode", { mode });
        },
        requestTeardown() {
            notify("ui/teardown", {});
        },
        log(level, message, data) {
            notify("ui/log", { level, message, data });
        },
        // Utility.
        isHosted() {
            return _connected;
        },
    };
    window.MCPApp = MCPApp;
    // Kick off the handshake.
    if (document.readyState === "loading") {
        document.addEventListener("DOMContentLoaded", initialize);
    }
    else {
        initialize();
    }
})();
