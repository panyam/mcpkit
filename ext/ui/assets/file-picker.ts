/**
 * SEP-2356 Phase 2.1 — in-iframe file picker primitives.
 *
 * The bridge ships these as `MCPApp.selectFile()` / `MCPApp.selectFiles()`,
 * but the picker logic — descriptor type, sentinel errors, the
 * pick → validate → encode pipeline — lives here so the protocol shell
 * (mcp-app-bridge.ts) stays focused on JSON-RPC and host messaging.
 *
 * Wire format: `data:<mediaType>;name=<pct-encoded-filename>;base64,<payload>`,
 * matching `core.EncodeDataURI` in the Go SDK byte-for-byte. A server can
 * decode the URI via `core.DecodeDataURI` without browser-side quirks.
 *
 * Activation note: the picker call MUST be invoked from inside a
 * user-gesture handler (button click, keypress, etc.). Modern browsers
 * block programmatic `<input>.click()` outside that context.
 */

/**
 * Mirrors the server-side `core.FileInputDescriptor`. The bridge enforces
 * the same `accept` / `maxSize` rules client-side so a server receiving the
 * resulting data URI can trust the payload already passed validation.
 */
export interface FileInputDescriptor {
  /**
   * MIME patterns or file extensions. Each entry is one of:
   *   - exact MIME ("image/png")
   *   - wildcard subtype ("image/*")
   *   - extension hint (".pdf")
   * Empty / omitted means any file is accepted.
   */
  accept?: string[];
  /** Max decoded payload size in bytes. Omitted means no client-side cap. */
  maxSize?: number;
}

/** Thrown when the user dismisses the file picker without choosing a file. */
export class MCPFileSelectionCanceled extends Error {
  constructor() {
    super("file selection canceled");
    this.name = "MCPFileSelectionCanceled";
  }
}

/**
 * Thrown when a chosen file exceeds `descriptor.maxSize`. `reason` matches
 * the server-side `-32602` reason from issue #361 so callers can branch on
 * the same constant on either side of the wire.
 */
export class MCPFileTooLarge extends Error {
  readonly reason = "file_too_large";
  constructor(public readonly size: number, public readonly maxSize: number) {
    super(`file size ${size} exceeds maxSize ${maxSize}`);
    this.name = "MCPFileTooLarge";
  }
}

/**
 * Thrown when a chosen file's MIME / extension is not in `descriptor.accept`.
 * `reason` aligns with server-side `-32602`.
 */
export class MCPFileTypeNotAccepted extends Error {
  readonly reason = "file_type_not_accepted";
  constructor(
    public readonly mediaType: string,
    public readonly accept: ReadonlyArray<string>
  ) {
    super(`file type ${mediaType} not in accept list [${accept.join(", ")}]`);
    this.name = "MCPFileTypeNotAccepted";
  }
}

/**
 * Percent-encode a string using the same character set as Go's
 * `url.PathEscape` so the bridge's data URI `name=` parameter is
 * byte-for-byte identical to `core.EncodeDataURI` output. JS's
 * `encodeURIComponent` leaves `( ) ! * '` unescaped while PathEscape
 * encodes them — this helper matches Go's choices so a server-side decode
 * round-trips bit-for-bit.
 */
export function pctEncodePathLike(s: string): string {
  return encodeURIComponent(s).replace(
    /[!'()*]/g,
    (ch) => "%" + ch.charCodeAt(0).toString(16).toUpperCase()
  );
}

/**
 * Match a file against an `accept` pattern list. Pattern rules:
 *   - exact MIME ("image/png") matches by `file.type` equality
 *   - wildcard subtype ("image/*") matches by `file.type` prefix
 *   - extension hint (".pdf") matches by `file.name` suffix (case-insensitive)
 * Empty / undefined `accept` means anything matches. Mirrors the
 * server-side validator behavior planned in issue #361.
 */
export function fileMatchesAccept(file: File, accept?: string[]): boolean {
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

/**
 * Open a hidden `<input type="file">` and resolve with the selected files
 * (or `null` on cancel). Must be invoked from inside a user-gesture event
 * handler — modern browsers reject programmatic `.click()` otherwise.
 *
 * The element is removed from the DOM when the picker resolves so the
 * iframe stays clean across multiple invocations.
 */
export function openFilePicker(
  accept: string[] | undefined,
  multiple: boolean
): Promise<File[] | null> {
  return new Promise((resolve) => {
    const input = document.createElement("input");
    input.type = "file";
    input.style.display = "none";
    if (accept && accept.length > 0) input.accept = accept.join(",");
    if (multiple) input.multiple = true;

    let settled = false;
    const settle = (value: File[] | null) => {
      if (settled) return;
      settled = true;
      try {
        input.remove();
      } catch {
        // best effort — DOM may have been torn down
      }
      resolve(value);
    };

    input.addEventListener("change", () => {
      settle(Array.from(input.files ?? []));
    });
    // Modern browsers fire `cancel` when the user dismisses the dialog
    // (Chrome 113+, Safari 17+, Firefox 91+). The focus-return fallback
    // below covers older paths.
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

/** FileReader → "data:<mediaType>;name=<pct-encoded>;base64,<...>". */
export function readAsDataURI(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      const raw = reader.result as string;
      // raw shape: "data:<mediaType>;base64,<payload>" — inject `name=` so
      // the wire envelope matches `core.EncodeDataURI`.
      const colonAt = raw.indexOf(":");
      const semiAt = raw.indexOf(";", colonAt);
      if (colonAt < 0 || semiAt < 0) {
        reject(new Error("FileReader returned malformed data URL"));
        return;
      }
      const mediaType = raw.slice(colonAt + 1, semiAt);
      const rest = raw.slice(semiAt); // ";base64,<payload>" (or ";<param>;base64,...")
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

/**
 * Run the full picker → validate → encode pipeline. Shared by
 * `selectFile` (returns one URI) and `selectFiles` (returns an array).
 */
export async function selectFilesInternal(
  descriptor: FileInputDescriptor | undefined,
  multiple: boolean
): Promise<string[]> {
  const desc = descriptor ?? {};
  const files = await openFilePicker(desc.accept, multiple);
  if (files === null || files.length === 0) {
    throw new MCPFileSelectionCanceled();
  }
  // Validate each file BEFORE running FileReader — saves a wasted decode
  // and matches the server-side fail-fast behavior planned for #361.
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
