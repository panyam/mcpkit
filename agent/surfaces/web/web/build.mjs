import { build } from "esbuild";
import { solidPlugin } from "esbuild-plugin-solid";
import { createRequire } from "module";
import { copyFileSync, existsSync, mkdirSync, readdirSync, rmSync } from "fs";

const require = createRequire(import.meta.url);

// The bundle is emitted into the Go module's static/ tree (agent/web/static),
// which shell.go embeds. Committing the built bundle keeps `just test-agent`
// and `go build` working with no Node step; a rebuild refreshes it in place.
const OUT = "../static";

if (!existsSync(OUT)) mkdirSync(OUT, { recursive: true });

// Clear previously emitted bundles first. splitting names lazy chunks with a
// content hash, so a rebuild would otherwise leave orphaned chunks beside the
// new ones. Only .js/.js.map are removed; dockview.css is recopied below.
for (const f of readdirSync(OUT)) {
  if (f.endsWith(".js") || f.endsWith(".js.map")) rmSync(`${OUT}/${f}`);
}

// Pin a single Solid reactive core. Two copies of solid-js make cross-island
// reactivity (a shared store's setState) silently no-op, so alias every entry
// to one physical file. Carry this guard forward as islands multiply (#1198).
const solidAlias = {
  "solid-js": require.resolve("solid-js/dist/solid.js"),
  "solid-js/web": require.resolve("solid-js/web/dist/web.js"),
  "solid-js/store": require.resolve("solid-js/store/dist/store.js"),
};

// splitting + outdir (not outfile) so the dynamic import of DockviewWorkspace
// (main.ts) becomes its own chunk: dockview-core loads only on the dockview
// layout, leaving the mobile path unaffected. The named entry keeps the output
// at static/app.js; lazy chunks land beside it and are fetched on demand.
await build({
  entryPoints: { app: "src/main.ts" },
  outdir: OUT,
  bundle: true,
  splitting: true,
  format: "esm",
  target: "es2022",
  minify: true,
  sourcemap: true,
  alias: solidAlias,
  mainFields: ["module", "browser", "main"],
  conditions: ["import", "browser", "default"],
  plugins: [solidPlugin()],
  logLevel: "info",
});

// Dockview's structural CSS is copied verbatim (not bundled) so the shell links
// it only on the dockview layout; the mobile page never requests it. Themes are
// applied in JS (themeLight/themeDark), not via CSS files.
copyFileSync(require.resolve("dockview-core/dist/styles/dockview.css"), `${OUT}/dockview.css`);
