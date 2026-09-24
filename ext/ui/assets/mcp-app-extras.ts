/**
 * mcpkit extras for MCP Apps Views, usable with any View runtime.
 *
 * The mcpkit bridge exposes these as `MCPApp.selectFile` / `selectFiles` and
 * `MCPApp.setTraceContextProvider`. This module offers the same code to Views
 * built on upstream's `App` from `@modelcontextprotocol/ext-apps`
 * (see docs/APPS_DESIGN.md § Frontend independence):
 *
 *     import { App, PostMessageTransport } from "@modelcontextprotocol/ext-apps";
 *     import { selectFile, withTraceRelay } from "./mcp-app-extras.js";
 *
 *     const app = new App({ name: "my-view", version: "1.0.0" });
 *     await app.connect(withTraceRelay(new PostMessageTransport(window.parent, window.parent), provider));
 *     const uri = await selectFile({ accept: ["image/*"], maxSize: 5_000_000 });
 */

export {
  selectFile,
  selectFiles,
  MCPFileSelectionCanceled,
  MCPFileTooLarge,
  MCPFileTypeNotAccepted,
  type FileInputDescriptor,
} from "./file-picker.js";

export {
  withTraceRelay,
  type TraceContext,
  type TraceContextProvider,
  type SendingTransport,
} from "./trace-relay.js";
