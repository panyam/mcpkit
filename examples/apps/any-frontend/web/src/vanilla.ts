// Upstream's App from @modelcontextprotocol/ext-apps, no framework.
import { App, PostMessageTransport } from "@modelcontextprotocol/ext-apps";
import { contextFor, elements, render, type Color } from "./dom";

const app = new App({ name: "any-frontend-vanilla", version: "0.1.0" });
let current: Color | null = null;

async function show(c: Color) {
  current = c;
  render(c);
  await app.updateModelContext(contextFor(c));
}

// Handlers go on before connect, so the initial tool result isn't missed.
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

await app.connect(new PostMessageTransport(window.parent, window.parent));
