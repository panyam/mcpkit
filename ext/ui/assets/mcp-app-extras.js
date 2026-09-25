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
function selectFile(descriptor) {
  return selectFilesInternal(descriptor, false).then((uris) => uris[0]);
}
function selectFiles(descriptor) {
  return selectFilesInternal(descriptor, true);
}

// trace-relay.ts
function resolveTraceContext(provider, logPrefix = "[mcpkit]") {
  if (!provider) return null;
  let tc;
  try {
    tc = provider();
  } catch (err) {
    if (typeof console !== "undefined") {
      console.warn(logPrefix + " traceContextProvider threw; skipping trace propagation:", err);
    }
    return null;
  }
  return tc && tc.traceparent ? tc : null;
}
function mergeTraceMeta(params, tc) {
  let merged;
  if (params === void 0 || params === null) {
    merged = {};
  } else if (typeof params === "object" && !Array.isArray(params)) {
    merged = { ...params };
  } else {
    return params;
  }
  const existing = merged._meta;
  const meta = existing && typeof existing === "object" && !Array.isArray(existing) ? { ...existing } : {};
  if (meta.traceparent === void 0 && tc.traceparent) meta.traceparent = tc.traceparent;
  if (meta.tracestate === void 0 && tc.tracestate) meta.tracestate = tc.tracestate;
  merged._meta = meta;
  return merged;
}
function withTraceRelay(transport, provider) {
  const send = transport.send.bind(transport);
  transport.send = ((message, options) => {
    if (message && typeof message === "object" && typeof message.method === "string") {
      const tc = resolveTraceContext(provider, "[mcpkit withTraceRelay]");
      if (tc) {
        message = { ...message, params: mergeTraceMeta(message.params, tc) };
      }
    }
    return send(message, options);
  });
  return transport;
}
export {
  MCPFileSelectionCanceled,
  MCPFileTooLarge,
  MCPFileTypeNotAccepted,
  selectFile,
  selectFiles,
  withTraceRelay
};
