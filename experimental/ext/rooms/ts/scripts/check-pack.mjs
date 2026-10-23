// Checks what `npm pack` would ship: the built entry points and README, and
// no tests or sources. The publish workflow refuses test files too; this runs
// on every PR so a broken package.json shows up before a release does.
import { execFileSync } from "node:child_process";

const out = JSON.parse(execFileSync("npm", ["pack", "--dry-run", "--json", "--ignore-scripts"], { encoding: "utf8" }));
// npm 10/11 print an array of packs, npm 12 an object keyed by package name.
const pack = Array.isArray(out) ? out[0] : Object.values(out)[0];
const files = pack.files.map((f) => f.path).sort();

const problems = [];
for (const need of ["package.json", "README.md", "dist/index.js", "dist/index.d.ts"]) {
  if (!files.includes(need)) problems.push(`missing ${need} (run the build first)`);
}
for (const f of files) {
  if (/\.test\./.test(f)) problems.push(`ships a test file: ${f}`);
  if (f.startsWith("src/") || f.startsWith("test/")) problems.push(`ships a source file: ${f}`);
}
if (problems.length) {
  console.error(`${pack.name}@${pack.version}:\n  ${problems.join("\n  ")}`);
  process.exit(1);
}
console.log(`${pack.name}@${pack.version}: ${files.length} files`);
