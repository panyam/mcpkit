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
    // Bidirectional handlers (host → app requests).
    let _oncalltool = null;
    let _onlisttools = null;
    // Tool registry for registerTool() API.
    const _registeredTools = new Map();
    let _useRegistry = false;
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
    function request(method, params, options) {
        if (!_connected && method !== "ui/initialize") {
            return Promise.reject(new Error("Not connected to MCP host"));
        }
        // Resolve signal: explicit signal > timeout shorthand > default 30s.
        let signal = options === null || options === void 0 ? void 0 : options.signal;
        if (!signal && (options === null || options === void 0 ? void 0 : options.timeout)) {
            signal = AbortSignal.timeout(options.timeout);
        }
        return new Promise((resolve, reject) => {
            var _a;
            const id = nextId++;
            const cleanup = () => { pending.delete(id); };
            pending.set(id, { resolve, reject });
            send({ jsonrpc: "2.0", id, method, params: params || {} });
            // AbortSignal-based cancellation.
            if (signal) {
                if (signal.aborted) {
                    cleanup();
                    reject(new Error(((_a = signal.reason) === null || _a === void 0 ? void 0 : _a.message) || "Aborted"));
                    return;
                }
                signal.addEventListener("abort", () => {
                    var _a;
                    if (pending.has(id)) {
                        cleanup();
                        reject(new Error(((_a = signal.reason) === null || _a === void 0 ? void 0 : _a.message) || "Aborted: " + method));
                    }
                }, { once: true });
            }
            else {
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
    function notify(method, params) {
        // Allow handshake notifications through before connected.
        if (!_connected && method !== "ui/notifications/initialized") {
            if (typeof console !== "undefined") {
                console.warn("[MCPApp] notify blocked before handshake: " + method);
            }
            return;
        }
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
        // Request from host (has both id and method) — bidirectional call.
        const req = msg;
        if (req.id != null && req.method) {
            handleHostRequest(req);
            return;
        }
        // Notification from host (no id, only method).
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
                applyHostStyles(_hostContext);
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
    // --- Style utilities -----------------------------------------------------
    function applyTheme(theme) {
        const root = document.documentElement;
        root.setAttribute("data-theme", theme);
        root.style.colorScheme = theme;
    }
    function applyStyleVariables(variables, root = document.documentElement) {
        for (const [key, value] of Object.entries(variables)) {
            if (value !== undefined) {
                root.style.setProperty(key, value);
            }
        }
    }
    const FONTS_STYLE_ID = "__mcp-host-fonts";
    function applyFonts(fontCss) {
        if (document.getElementById(FONTS_STYLE_ID))
            return;
        const style = document.createElement("style");
        style.id = FONTS_STYLE_ID;
        style.textContent = fontCss;
        document.head.appendChild(style);
    }
    /** Apply all available styles from the host context. */
    function applyHostStyles(ctx) {
        var _a, _b, _c;
        if (ctx.theme)
            applyTheme(ctx.theme);
        if ((_a = ctx.styles) === null || _a === void 0 ? void 0 : _a.variables)
            applyStyleVariables(ctx.styles.variables);
        if ((_c = (_b = ctx.styles) === null || _b === void 0 ? void 0 : _b.css) === null || _c === void 0 ? void 0 : _c.fonts)
            applyFonts(ctx.styles.css.fonts);
    }
    // --- Bidirectional handlers (host → app requests) -----------------------
    /** Respond to a JSON-RPC request from the host. */
    function respond(id, result, error) {
        if (error) {
            send({
                jsonrpc: "2.0",
                id,
                error: {
                    code: -32000,
                    message: String(error),
                },
            });
        }
        else {
            send({ jsonrpc: "2.0", id, result });
        }
    }
    async function handleHostRequest(req) {
        if (req.id == null)
            return; // Not a request, just a notification.
        const id = req.id;
        const params = (req.params || {});
        try {
            switch (req.method) {
                case "tools/call": {
                    if (_useRegistry) {
                        const name = params.name || "";
                        const tool = _registeredTools.get(name);
                        if (tool && tool.enabled) {
                            const args = params.arguments || {};
                            // Standard Schema validation (if inputSchema implements ~standard).
                            if (tool.validate) {
                                const vResult = tool.validate(args);
                                if ("issues" in vResult) {
                                    const msgs = vResult.issues.map((i) => { var _a; return (((_a = i.path) === null || _a === void 0 ? void 0 : _a.map((p) => p.key).join(".")) || "") + ": " + i.message; }).join("; ");
                                    respond(id, null, "Validation failed: " + msgs);
                                    break;
                                }
                            }
                            const result = await tool.handler(args);
                            respond(id, result);
                        }
                        else {
                            respond(id, null, "Unknown tool: " + name);
                        }
                    }
                    else if (_oncalltool) {
                        const result = await _oncalltool({
                            name: params.name || "",
                            arguments: params.arguments || {},
                        });
                        respond(id, result);
                    }
                    else {
                        respond(id, null, "No oncalltool handler registered");
                    }
                    break;
                }
                case "tools/list": {
                    if (_useRegistry) {
                        const tools = [];
                        _registeredTools.forEach((t) => {
                            if (!t.enabled)
                                return;
                            const entry = { name: t.name };
                            if (t.description !== undefined)
                                entry.description = t.description;
                            if (t.inputSchema !== undefined)
                                entry.inputSchema = t.inputSchema;
                            if (t.outputSchema !== undefined)
                                entry.outputSchema = t.outputSchema;
                            tools.push(entry);
                        });
                        respond(id, { tools });
                    }
                    else if (_onlisttools) {
                        const tools = await _onlisttools();
                        respond(id, { tools });
                    }
                    else {
                        respond(id, { tools: [] });
                    }
                    break;
                }
                default:
                    respond(id, null, "Method not found: " + req.method);
                    break;
            }
        }
        catch (e) {
            respond(id, null, e instanceof Error ? e.message : String(e));
        }
    }
    // --- Tool registration API ------------------------------------------------
    /** Send notifications/tools/list_changed to the host. */
    function sendToolListChanged() {
        notify("notifications/tools/list_changed", {});
    }
    /**
     * Extract a Standard Schema v1 validate function from a schema object.
     * Returns undefined if the schema doesn't implement the ~standard protocol.
     * See https://standardschema.dev/ for the spec.
     */
    function extractStandardValidate(schema) {
        if (schema == null || typeof schema !== "object")
            return undefined;
        // Standard Schema v1 uses the "~standard" property.
        const std = schema["~standard"];
        if (std && typeof std === "object" && typeof std.validate === "function") {
            return (value) => std.validate(value);
        }
        return undefined;
    }
    /**
     * Register an app-provided tool that the host/model can call.
     * Installs auto-dispatch handlers for tools/call and tools/list,
     * replacing any manually set oncalltool/onlisttools handlers.
     *
     * If inputSchema implements the Standard Schema protocol (~standard.validate),
     * input arguments are validated before the handler is called.
     */
    function registerTool(name, config, handler) {
        _registeredTools.set(name, {
            name,
            description: config.description,
            inputSchema: config.inputSchema,
            outputSchema: config.outputSchema,
            enabled: true,
            handler,
            validate: extractStandardValidate(config.inputSchema),
        });
        _useRegistry = true;
        sendToolListChanged();
        return {
            update(partial) {
                const tool = _registeredTools.get(name);
                if (!tool)
                    return;
                if (partial.description !== undefined)
                    tool.description = partial.description;
                if (partial.inputSchema !== undefined) {
                    tool.inputSchema = partial.inputSchema;
                    tool.validate = extractStandardValidate(partial.inputSchema);
                }
                if (partial.outputSchema !== undefined)
                    tool.outputSchema = partial.outputSchema;
                sendToolListChanged();
            },
            disable() {
                const tool = _registeredTools.get(name);
                if (tool) {
                    tool.enabled = false;
                    sendToolListChanged();
                }
            },
            enable() {
                const tool = _registeredTools.get(name);
                if (tool) {
                    tool.enabled = true;
                    sendToolListChanged();
                }
            },
            remove() {
                _registeredTools.delete(name);
                if (_registeredTools.size === 0)
                    _useRegistry = false;
                sendToolListChanged();
            },
        };
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
            // Auto-apply host styles on connect.
            applyHostStyles(_hostContext);
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
        // Host-bound methods (all support optional RequestOptions).
        callTool(name, args, options) {
            return request("tools/call", {
                name,
                arguments: args || {},
            }, options);
        },
        readResource(uri, options) {
            return request("resources/read", { uri }, options);
        },
        sendMessage(message, options) {
            return request("ui/message", message, options);
        },
        updateModelContext(context, options) {
            return request("ui/update-model-context", { context }, options);
        },
        openLink(url, options) {
            return request("ui/open-link", { url }, options);
        },
        downloadFile(url, filename, options) {
            return request("ui/download-file", { url, filename }, options);
        },
        requestDisplayMode(mode, options) {
            return request("ui/request-display-mode", { mode }, options);
        },
        requestTeardown() {
            notify("ui/teardown", {});
        },
        log(level, message, data) {
            notify("ui/log", { level, message, data });
        },
        // Style utilities.
        applyTheme,
        applyStyleVariables,
        applyFonts,
        applyHostStyles,
        // Bidirectional handlers (set these before connecting).
        set oncalltool(handler) { _oncalltool = handler; },
        get oncalltool() { return _oncalltool; },
        set onlisttools(handler) { _onlisttools = handler; },
        get onlisttools() { return _onlisttools; },
        // Tool registration API (higher-level than oncalltool/onlisttools).
        registerTool,
        sendToolListChanged,
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
