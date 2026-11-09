// Bundles the page into ../static/app.js, which the Go server embeds. Run
// with `make build-web` and commit the output, so `go run .` needs no Node.
import { build } from "esbuild";

await build({
  entryPoints: ["src/main.ts"],
  bundle: true,
  format: "esm",
  target: "es2022",
  minify: true,
  legalComments: "none",
  outfile: "../static/app.js",
  logLevel: "warning",
});
