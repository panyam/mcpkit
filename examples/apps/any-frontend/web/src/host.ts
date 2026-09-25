// A minimal MCP Apps host, served by the Go example at /host.
//
// It uses upstream's reference AppBridge to render every pick_color_* View
// side by side, so the example can be seen without installing a host, and
// the e2e test drives the same page. Unlike a production host it loads each
// View into a single sandboxed srcdoc iframe, without the double-iframe
// sandbox proxy a real host should use.
import { AppBridge, PostMessageTransport, getToolUiResourceUri } from "@modelcontextprotocol/ext-apps/app-bridge";
import { Client, StreamableHTTPClientTransport } from "@modelcontextprotocol/client";

interface HostState {
  views: Record<
    string,
    { initialized: boolean; lastContext: unknown; contextUpdates: number; toolCallMeta: unknown[] }
  >;
  errors: string[];
  ready: boolean;
}
const state: HostState = { views: {}, errors: [], ready: false };
(window as unknown as { __anyFrontend: HostState }).__anyFrontend = state;

const HOST_INFO = { name: "any-frontend-host", version: "0.1.0" };

function panel(tool: string, title: string): HTMLIFrameElement {
  const section = document.createElement("section");
  section.dataset.tool = tool;
  const h = document.createElement("h2");
  h.textContent = title;
  const frame = document.createElement("iframe");
  frame.setAttribute("sandbox", "allow-scripts");
  frame.title = title;
  frame.dataset.tool = tool;
  const ctx = document.createElement("pre");
  ctx.dataset.testid = "model-context";
  ctx.textContent = "model context: (none yet)";
  section.append(h, frame, ctx);
  document.getElementById("views")!.append(section);
  return frame;
}

async function mount(client: Client, tool: { name: string; title?: string; _meta?: unknown }) {
  const uri = getToolUiResourceUri(tool as Parameters<typeof getToolUiResourceUri>[0]);
  if (!uri) return;
  const view: HostState["views"][string] = { initialized: false, lastContext: null, contextUpdates: 0, toolCallMeta: [] };
  state.views[tool.name] = view;
  const frame = panel(tool.name, tool.title ?? tool.name);
  // Record the raw _meta of each tools/call the View sends, before AppBridge
  // relays it, so the test can see what a View's runtime puts on the wire.
  window.addEventListener("message", (e) => {
    const msg = e.data as { method?: string; params?: { _meta?: unknown } } | null;
    if (e.source === frame.contentWindow && msg?.method === "tools/call") {
      view.toolCallMeta.push(msg.params?._meta ?? null);
    }
  });
  const ctxEl = frame.parentElement!.querySelector("pre")!;

  const bridge = new AppBridge(client, HOST_INFO, {
    serverTools: {},
    openLinks: {},
    updateModelContext: { text: {} },
  }, { hostContext: { theme: "light", platform: "web", displayMode: "inline" } });

  bridge.onupdatemodelcontext = async (params) => {
    view.lastContext = params;
    view.contextUpdates++;
    ctxEl.textContent = "model context: " + JSON.stringify(params.structuredContent ?? params.content);
    return {};
  };
  const initialized = new Promise<void>((resolve) => {
    bridge.oninitialized = () => {
      view.initialized = true;
      resolve();
    };
  });

  await bridge.connect(new PostMessageTransport(frame.contentWindow!, frame.contentWindow!));
  const [resource, result] = await Promise.all([
    client.readResource({ uri }),
    client.callTool({ name: tool.name, arguments: {} }),
  ]);
  const html = (resource.contents[0] as { text?: string }).text ?? "";
  frame.srcdoc = html;
  await initialized;
  bridge.sendToolInput({ arguments: {} });
  bridge.sendToolResult(result as Parameters<typeof bridge.sendToolResult>[0]);
}

async function main() {
  const client = new Client(HOST_INFO);
  await client.connect(new StreamableHTTPClientTransport(new URL("/mcp", location.href)));
  const { tools } = await client.listTools();
  const pickers = tools.filter((t) => t.name.startsWith("pick_color_")).sort((a, b) => a.name.localeCompare(b.name));
  await Promise.all(pickers.map((t) => mount(client, t).catch((e) => state.errors.push(`${t.name}: ${e}`))));
  state.ready = true;
  document.getElementById("status")!.textContent = `${pickers.length} Views mounted`;
}

main().catch((e) => {
  state.errors.push(String(e));
  document.getElementById("status")!.textContent = `error: ${e}`;
});
