"use strict";
(() => {
  // file-picker.ts
  var MCPFileSelectionCanceled = class extends Error {
    constructor() {
      super("file selection canceled");
      this.name = "MCPFileSelectionCanceled";
    }
  };
  var MCPFileTooLarge = class extends Error {
    constructor(size, maxSize) {
      super(`file size ${size} exceeds maxSize ${maxSize}`);
      this.size = size;
      this.maxSize = maxSize;
      this.reason = "file_too_large";
      this.name = "MCPFileTooLarge";
    }
  };
  var MCPFileTypeNotAccepted = class extends Error {
    constructor(mediaType, accept) {
      super(`file type ${mediaType} not in accept list [${accept.join(", ")}]`);
      this.mediaType = mediaType;
      this.accept = accept;
      this.reason = "file_type_not_accepted";
      this.name = "MCPFileTypeNotAccepted";
    }
  };
  function pctEncodePathLike(s) {
    return encodeURIComponent(s).replace(
      /[!'()*]/g,
      (ch) => "%" + ch.charCodeAt(0).toString(16).toUpperCase()
    );
  }
  function fileMatchesAccept(file, accept) {
    if (!accept || accept.length === 0) return true;
    const lowerName = file.name.toLowerCase();
    for (const pattern of accept) {
      if (pattern.startsWith(".")) {
        if (lowerName.endsWith(pattern.toLowerCase())) return true;
        continue;
      }
      const slash = pattern.indexOf("/");
      if (slash < 0) continue;
      const subtype = pattern.slice(slash + 1);
      if (subtype === "*") {
        if (file.type.startsWith(pattern.slice(0, slash + 1))) return true;
      } else if (file.type === pattern) {
        return true;
      }
    }
    return false;
  }
  function openFilePicker(accept, multiple) {
    return new Promise((resolve) => {
      const input = document.createElement("input");
      input.type = "file";
      input.style.display = "none";
      if (accept && accept.length > 0) input.accept = accept.join(",");
      if (multiple) input.multiple = true;
      let settled = false;
      const settle = (value) => {
        if (settled) return;
        settled = true;
        try {
          input.remove();
        } catch {
        }
        resolve(value);
      };
      input.addEventListener("change", () => {
        settle(Array.from(input.files ?? []));
      });
      input.addEventListener("cancel", () => settle(null));
      const onFocus = () => {
        setTimeout(() => {
          if (!settled && (input.files == null || input.files.length === 0)) {
            settle(null);
          }
        }, 300);
        window.removeEventListener("focus", onFocus);
      };
      window.addEventListener("focus", onFocus);
      document.body.appendChild(input);
      input.click();
    });
  }
  function readAsDataURI(file) {
    return new Promise((resolve, reject) => {
      const reader = new FileReader();
      reader.onload = () => {
        const raw = reader.result;
        const colonAt = raw.indexOf(":");
        const semiAt = raw.indexOf(";", colonAt);
        if (colonAt < 0 || semiAt < 0) {
          reject(new Error("FileReader returned malformed data URL"));
          return;
        }
        const mediaType = raw.slice(colonAt + 1, semiAt);
        const rest = raw.slice(semiAt);
        if (!file.name) {
          resolve(`data:${mediaType}${rest}`);
          return;
        }
        resolve(`data:${mediaType};name=${pctEncodePathLike(file.name)}${rest}`);
      };
      reader.onerror = () => reject(reader.error ?? new Error("FileReader error"));
      reader.readAsDataURL(file);
    });
  }
  async function selectFilesInternal(descriptor, multiple) {
    const desc = descriptor ?? {};
    const files = await openFilePicker(desc.accept, multiple);
    if (files === null || files.length === 0) {
      throw new MCPFileSelectionCanceled();
    }
    for (const file of files) {
      if (desc.maxSize != null && file.size > desc.maxSize) {
        throw new MCPFileTooLarge(file.size, desc.maxSize);
      }
      if (!fileMatchesAccept(file, desc.accept)) {
        throw new MCPFileTypeNotAccepted(file.type, desc.accept ?? []);
      }
    }
    return Promise.all(files.map((f) => readAsDataURI(f)));
  }

  // mcp-app-bridge.ts
  (function() {
    "use strict";
    if (window.MCPApp) return;
    const config = window.MCPAppConfig || {};
    const APP_NAME = config.name || "mcp-app";
    const APP_VERSION = config.version || "0.0.0";
    const PROTOCOL_VERSION = config.protocolVersion || "2026-01-26";
    let nextId = 1;
    const pending = /* @__PURE__ */ new Map();
    const listeners = /* @__PURE__ */ new Map();
    let _connected = false;
    let _hostContext = null;
    let _hostCapabilities = null;
    let _oncalltool = null;
    let _onlisttools = null;
    const _registeredTools = /* @__PURE__ */ new Map();
    let _useRegistry = false;
    function on(event, handler) {
      let set = listeners.get(event);
      if (!set) {
        set = /* @__PURE__ */ new Set();
        listeners.set(event, set);
      }
      set.add(handler);
      return () => set.delete(handler);
    }
    function off(event, handler) {
      listeners.get(event)?.delete(handler);
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
          } catch (e) {
            console.error("[MCPApp] handler error for " + event + ":", e);
          }
        });
      }
      try {
        document.dispatchEvent(
          new CustomEvent("mcp:" + event, { detail: data })
        );
      } catch (_) {
      }
    }
    function send(msg) {
      if (window.parent && window.parent !== window) {
        window.parent.postMessage(msg, "*");
      }
    }
    function request(method, params, options) {
      if (!_connected && method !== "ui/initialize") {
        return Promise.reject(new Error("Not connected to MCP host"));
      }
      let signal = options?.signal;
      if (!signal && options?.timeout) {
        signal = AbortSignal.timeout(options.timeout);
      }
      return new Promise((resolve, reject) => {
        const id = nextId++;
        const cleanup = () => {
          pending.delete(id);
        };
        pending.set(id, { resolve, reject });
        send({ jsonrpc: "2.0", id, method, params: params || {} });
        if (signal) {
          if (signal.aborted) {
            cleanup();
            reject(new Error(signal.reason?.message || "Aborted"));
            return;
          }
          signal.addEventListener("abort", () => {
            if (pending.has(id)) {
              cleanup();
              reject(new Error(signal.reason?.message || "Aborted: " + method));
            }
          }, { once: true });
        } else {
          setTimeout(() => {
            if (pending.has(id)) {
              cleanup();
              reject(new Error("Request timeout: " + method));
            }
          }, 3e4);
        }
      });
    }
    function notify(method, params) {
      if (!_connected && method !== "ui/notifications/initialized") {
        if (typeof console !== "undefined") {
          console.warn("[MCPApp] notify blocked before handshake: " + method);
        }
        return;
      }
      send({ jsonrpc: "2.0", method, params: params || {} });
    }
    function isJsonRpc(data) {
      return typeof data === "object" && data !== null && data.jsonrpc === "2.0";
    }
    function handleMessage(event) {
      const msg = event.data;
      if (!isJsonRpc(msg)) return;
      if ("id" in msg && msg.id != null && !("method" in msg)) {
        const resp = msg;
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
      const req = msg;
      if (req.id != null && req.method) {
        handleHostRequest(req);
        return;
      }
      switch (req.method) {
        case "ui/notifications/tool-input": {
          const p = req.params || {};
          emit("toolinput", { tool: p.name || "", arguments: p.arguments || {} });
          break;
        }
        case "ui/notifications/tool-input-partial": {
          const p = req.params || {};
          emit("toolinputpartial", {
            tool: p.name || "",
            arguments: p.arguments || {}
          });
          break;
        }
        case "ui/notifications/tool-result": {
          const p = req.params || {};
          emit("toolresult", { tool: p.name || "", result: p });
          break;
        }
        case "ui/notifications/tool-cancelled": {
          const p = req.params || {};
          emit("toolcancelled", { tool: p.name || "" });
          break;
        }
        case "ui/notifications/host-context-changed": {
          const p = req.params || {};
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
          break;
      }
    }
    window.addEventListener("message", handleMessage);
    let resizeRaf = 0;
    function setupResizeObserver() {
      if (typeof ResizeObserver === "undefined") return;
      const observer = new ResizeObserver(() => {
        if (resizeRaf) cancelAnimationFrame(resizeRaf);
        resizeRaf = requestAnimationFrame(() => {
          const body = document.body;
          if (!body) return;
          notify("ui/notifications/size-changed", {
            width: body.scrollWidth,
            height: body.scrollHeight
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
    function applyTheme(theme) {
      const root = document.documentElement;
      root.setAttribute("data-theme", theme);
      root.style.colorScheme = theme;
    }
    function applyStyleVariables(variables, root = document.documentElement) {
      for (const [key, value] of Object.entries(variables)) {
        if (value !== void 0) {
          root.style.setProperty(key, value);
        }
      }
    }
    const FONTS_STYLE_ID = "__mcp-host-fonts";
    function applyFonts(fontCss) {
      if (document.getElementById(FONTS_STYLE_ID)) return;
      const style = document.createElement("style");
      style.id = FONTS_STYLE_ID;
      style.textContent = fontCss;
      document.head.appendChild(style);
    }
    function applyHostStyles(ctx) {
      if (ctx.theme) applyTheme(ctx.theme);
      if (ctx.styles?.variables) applyStyleVariables(ctx.styles.variables);
      if (ctx.styles?.css?.fonts) applyFonts(ctx.styles.css.fonts);
    }
    function respond(id, result, error) {
      if (error) {
        send({
          jsonrpc: "2.0",
          id,
          error: {
            code: -32e3,
            message: String(error)
          }
        });
      } else {
        send({ jsonrpc: "2.0", id, result });
      }
    }
    async function handleHostRequest(req) {
      if (req.id == null) return;
      const id = req.id;
      const params = req.params || {};
      try {
        switch (req.method) {
          case "tools/call": {
            if (_useRegistry) {
              const name = params.name || "";
              const tool = _registeredTools.get(name);
              if (tool && tool.enabled) {
                const args = params.arguments || {};
                if (tool.validate) {
                  const vResult = tool.validate(args);
                  if ("issues" in vResult) {
                    const msgs = vResult.issues.map(
                      (i) => (i.path?.map((p) => p.key).join(".") || "") + ": " + i.message
                    ).join("; ");
                    respond(id, null, "Validation failed: " + msgs);
                    break;
                  }
                }
                const result = await tool.handler(args);
                respond(id, result);
              } else {
                respond(id, null, "Unknown tool: " + name);
              }
            } else if (_oncalltool) {
              const result = await _oncalltool({
                name: params.name || "",
                arguments: params.arguments || {}
              });
              respond(id, result);
            } else {
              respond(id, null, "No oncalltool handler registered");
            }
            break;
          }
          case "tools/list": {
            if (_useRegistry) {
              const tools = [];
              _registeredTools.forEach((t) => {
                if (!t.enabled) return;
                const entry = { name: t.name };
                if (t.description !== void 0) entry.description = t.description;
                if (t.inputSchema !== void 0) entry.inputSchema = t.inputSchema;
                if (t.outputSchema !== void 0) entry.outputSchema = t.outputSchema;
                tools.push(entry);
              });
              respond(id, { tools });
            } else if (_onlisttools) {
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
    function sendToolListChanged() {
      notify("notifications/tools/list_changed", {});
    }
    function extractStandardValidate(schema) {
      if (schema == null || typeof schema !== "object") return void 0;
      const std = schema["~standard"];
      if (std && typeof std === "object" && typeof std.validate === "function") {
        return (value) => std.validate(value);
      }
      return void 0;
    }
    function registerTool(name, config2, handler) {
      _registeredTools.set(name, {
        name,
        description: config2.description,
        inputSchema: config2.inputSchema,
        outputSchema: config2.outputSchema,
        enabled: true,
        handler,
        validate: extractStandardValidate(config2.inputSchema)
      });
      _useRegistry = true;
      sendToolListChanged();
      return {
        update(partial) {
          const tool = _registeredTools.get(name);
          if (!tool) return;
          if (partial.description !== void 0) tool.description = partial.description;
          if (partial.inputSchema !== void 0) {
            tool.inputSchema = partial.inputSchema;
            tool.validate = extractStandardValidate(partial.inputSchema);
          }
          if (partial.outputSchema !== void 0) tool.outputSchema = partial.outputSchema;
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
          if (_registeredTools.size === 0) _useRegistry = false;
          sendToolListChanged();
        }
      };
    }
    function initialize() {
      if (window.parent === window) return;
      const timeout = setTimeout(() => {
        _connected = false;
      }, 2e3);
      request("ui/initialize", {
        protocolVersion: PROTOCOL_VERSION,
        appInfo: { name: APP_NAME, version: APP_VERSION },
        appCapabilities: {}
      }).then((result) => {
        clearTimeout(timeout);
        _connected = true;
        _hostContext = result?.hostContext || result || {};
        _hostCapabilities = result?.capabilities || {};
        applyHostStyles(_hostContext);
        emit("connected", {
          hostContext: _hostContext,
          capabilities: _hostCapabilities
        });
        notify("ui/notifications/initialized", { initialized: true });
        setupResizeObserver();
      }).catch(() => {
        clearTimeout(timeout);
        _connected = false;
      });
    }
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
          arguments: args || {}
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
      /**
       * Open a native file picker in the iframe and resolve with the chosen
       * file as an RFC 2397 base64 data URI (`data:<mediaType>;name=<...>;base64,<...>`).
       *
       * MUST be invoked from inside a user-gesture handler (button click,
       * keypress, etc.) — modern browsers block programmatic .click() on
       * file inputs otherwise.
       *
       * Wire format matches `core.EncodeDataURI` byte-for-byte so a server
       * receiving the URI can decode it via `core.DecodeDataURI` without
       * special-casing for browser-side encoding quirks.
       */
      selectFile(descriptor) {
        return selectFilesInternal(descriptor, false).then((uris) => uris[0]);
      },
      /**
       * Open a multi-select file picker. Same wire format as `selectFile`;
       * resolves with an array of data URIs in selection order.
       */
      selectFiles(descriptor) {
        return selectFilesInternal(descriptor, true);
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
      set oncalltool(handler) {
        _oncalltool = handler;
      },
      get oncalltool() {
        return _oncalltool;
      },
      set onlisttools(handler) {
        _onlisttools = handler;
      },
      get onlisttools() {
        return _onlisttools;
      },
      // Tool registration API (higher-level than oncalltool/onlisttools).
      registerTool,
      sendToolListChanged,
      // Utility.
      isHosted() {
        return _connected;
      }
    };
    window.MCPApp = MCPApp;
    if (document.readyState === "loading") {
      document.addEventListener("DOMContentLoaded", initialize);
    } else {
      initialize();
    }
  })();
})();
