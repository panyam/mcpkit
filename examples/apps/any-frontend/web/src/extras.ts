// Upstream's App plus mcpkit's extras (issue 1475): the transport is wrapped
// with withTraceRelay, so every outbound request carries _meta.traceparent
// for the Go server's tracing to pick up, and the file picker is available
// as selectFile. Everything else is the plain upstream runtime.
import { App, PostMessageTransport } from "@modelcontextprotocol/ext-apps";
import { selectFile, withTraceRelay } from "../../../../../ext/ui/assets/mcp-app-extras";
import { contextFor, elements, render, type Color } from "./dom";

const app = new App({ name: "any-frontend-extras", version: "0.1.0" });
let current: Color | null = null;

function newTraceparent(): string {
  const hex = (n: number) =>
    Array.from(crypto.getRandomValues(new Uint8Array(n)), (b) => b.toString(16).padStart(2, "0")).join("");
  return `00-${hex(16)}-${hex(8)}-01`;
}

async function show(c: Color) {
  current = c;
  render(c);
  await app.updateModelContext(contextFor(c));
}

app.ontoolresult = (result) => {
  if (result.structuredContent) void show(result.structuredContent as unknown as Color);
};

const { button, status } = elements();
button.addEventListener("click", async () => {
  try {
    const r = await app.callServerTool({ name: "shade_color", arguments: { hex: current!.hex } });
    await show(r.structuredContent as unknown as Color);
    status.textContent = "shaded by the server";
  } catch (e) {
    status.textContent = `error: ${(e as Error).message}`;
  }
});

// The picker is here to show the import works on upstream's runtime. It
// resolves to a data URI a Go server decodes with core.DecodeDataURI.
document.querySelector("[data-testid=pick-file]")?.addEventListener("click", async () => {
  try {
    const uri = await selectFile({ accept: ["image/*"], maxSize: 1_000_000 });
    status.textContent = `picked ${uri.slice(0, 40)}…`;
  } catch (e) {
    status.textContent = (e as Error).name;
  }
});

const transport = withTraceRelay(new PostMessageTransport(window.parent, window.parent), () => ({
  traceparent: newTraceparent(),
}));
await app.connect(transport);
