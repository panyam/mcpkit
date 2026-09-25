// Upstream's React bindings: useApp from @modelcontextprotocol/ext-apps/react.
import { useState } from "react";
import { createRoot } from "react-dom/client";
import { useApp } from "@modelcontextprotocol/ext-apps/react";
import type { App } from "@modelcontextprotocol/ext-apps";
import { contextFor, type Color } from "./dom";

function ColorPicker() {
  const [color, setColor] = useState<Color | null>(null);
  const [status, setStatus] = useState("");

  const show = (app: App, c: Color) => {
    setColor(c);
    void app.updateModelContext(contextFor(c));
  };

  const { app } = useApp({
    appInfo: { name: "any-frontend-react", version: "0.1.0" },
    capabilities: {},
    onAppCreated: (a) => {
      a.ontoolresult = (result) => {
        if (result.structuredContent) show(a, result.structuredContent as unknown as Color);
      };
    },
  });

  const darker = async () => {
    if (!app || !color) return;
    try {
      const r = await app.callServerTool({ name: "shade_color", arguments: { hex: color.hex } });
      show(app, r.structuredContent as unknown as Color);
      setStatus("shaded by the server");
    } catch (e) {
      setStatus(`error: ${(e as Error).message}`);
    }
  };

  return (
    <>
      <div className="runtime" data-testid="runtime">upstream React useApp</div>
      <div
        id="swatch"
        data-testid="swatch"
        data-hex={color?.hex}
        style={{ background: color?.hex }}
      />
      <p data-testid="label">{color ? `${color.name} ${color.hex}` : "waiting for a color…"}</p>
      <button data-testid="darker" disabled={!color} onClick={darker}>Darker</button>
      <p data-testid="status">{status}</p>
    </>
  );
}

createRoot(document.getElementById("root")!).render(<ColorPicker />);
