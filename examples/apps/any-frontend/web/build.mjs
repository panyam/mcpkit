// Builds each upstream-runtime View and the host page into one self-contained
// HTML file under ../views/, which the Go server embeds. Run with
// `make build-views` and commit the output, so `go run .` needs no Node.
import { build } from "esbuild";
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

// zod re-exports every error-message locale (about 250 KB minified) from
// v4/locales/index.js, and nothing in a View uses them. zod itself loads
// English directly from locales/en.js, so the index can export just that.
// Same problem as upstream ext-apps issue 665.
const zodEnglishOnly = {
  name: "zod-english-only",
  setup(b) {
    b.onResolve({ filter: /locales\/index\.js$/ }, (args) =>
      args.importer.includes("/zod/") ? { path: join(args.resolveDir, "../locales/index.js"), namespace: "zod-en" } : undefined,
    );
    b.onLoad({ filter: /.*/, namespace: "zod-en" }, (args) => ({
      contents: `export { default as en } from ${JSON.stringify(join(dirname(args.path), "en.js"))};`,
      resolveDir: dirname(args.path),
    }));
  },
};

const STYLE = `
  body { font: 14px system-ui, sans-serif; margin: 12px; }
  #swatch { width: 120px; height: 60px; border-radius: 6px; border: 1px solid #0003; }
  .runtime { font-size: 12px; opacity: .7; }`;

const MARKUP = (runtime, extra = "") => `
  <div class="runtime" data-testid="runtime">${runtime}</div>
  <div id="swatch" data-testid="swatch"></div>
  <p data-testid="label">waiting for a color…</p>
  <button data-testid="darker" disabled>Darker</button>${extra}
  <p data-testid="status"></p>`;

const pages = [
  { entry: "src/vanilla.ts", out: "upstream-vanilla.html", title: "Color picker (upstream App)", body: MARKUP("upstream App") },
  { entry: "src/react.tsx", out: "upstream-react.html", title: "Color picker (upstream React)", body: `<div id="root"></div>` },
  {
    entry: "src/extras.ts",
    out: "upstream-extras.html",
    title: "Color picker (upstream App + mcpkit extras)",
    body: MARKUP("upstream App + mcpkit extras", `\n  <button data-testid="pick-file">Pick an image</button>`),
  },
  {
    entry: "src/host.ts",
    out: "host.html",
    title: "any-frontend reference host",
    style: `
  body { font: 14px system-ui, sans-serif; margin: 16px; }
  #views { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 16px; }
  section { border: 1px solid #ddd; border-radius: 8px; padding: 8px; }
  h2 { font-size: 14px; margin: 0 0 8px; }
  iframe { width: 100%; height: 230px; border: 0; }
  pre { font-size: 11px; white-space: pre-wrap; margin: 4px 0 0; }`,
    body: `<h1>One Go backend, four frontends</h1>\n<p id="status">connecting…</p>\n<div id="views"></div>`,
  },
];

for (const p of pages) {
  const result = await build({
    entryPoints: [p.entry],
    bundle: true,
    format: "esm",
    target: "es2022",
    minify: true,
    write: false,
    jsx: "automatic",
    define: { "process.env.NODE_ENV": '"production"' },
    legalComments: "none",
    plugins: [zodEnglishOnly],
    logLevel: "warning",
  });
  // "</script" inside the bundle would end the inline tag early.
  const js = result.outputFiles[0].text.replaceAll("</script", "<\\/script");
  const html = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>${p.title}</title>
<style>${p.style ?? STYLE}
</style>
</head>
<body>
${p.body}
<script type="module">${js}</script>
</body>
</html>
`;
  writeFileSync(new URL(`../views/${p.out}`, import.meta.url), html);
  console.log(`views/${p.out}  ${(html.length / 1024).toFixed(1)} KB`);
}
