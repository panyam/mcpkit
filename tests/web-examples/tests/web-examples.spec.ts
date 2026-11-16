import { test, expect, type Page, type Locator } from "@playwright/test";
import { readdirSync, readFileSync } from "node:fs";

// One test per page and wire that scripts/collect_web_examples.sh produced.
// Each page's smoke call and wires come from its web/playground.json, which
// the collector publishes next to the page, so a new browser-ready example
// needs no edit here.

type Smoke = {
  tool: string;
  group?: string; // open this group first, for servers that list the tool only under groups/get
  args?: Record<string, string>;
  files?: Record<string, { name: string; mimeType: string; text: string }>; // attached to file inputs (SEP-2356)
  replies?: string[]; // buttons to press in order after the call, e.g. "Send responses"
  expect: string; // text the result must contain
};
type Wire = "stateless" | "session";
// Complete types into a prompt argument and expects a completion/complete suggestion.
type Complete = { prompt: string; argument: string; value: string; expect: string };
// Nested opens each parent group and expects the child inside it, with the child's own groups/get text.
type Nested = { parents: string[]; child: string; expect: string };
type Manifest = {
  title: string; summary: string; about: string[]; docs?: string; package?: string; wires?: Wire[]; smoke?: Smoke; complete?: Complete;
  nested?: Nested;
};

const examples = readdirSync(new URL("../.site/try/", import.meta.url), { withFileTypes: true })
  .filter((d) => d.isDirectory())
  .map((d) => d.name);
const manifest = (name: string): Manifest =>
  JSON.parse(readFileSync(new URL(`../.site/try/${name}/playground.json`, import.meta.url), "utf8"));
const wiresOf = (name: string): Wire[] => manifest(name).wires ?? ["stateless", "session"];

test("every example with a web/playground.json was collected", () => {
  const root = new URL("../../../examples/", import.meta.url);
  const manifests = (readdirSync(root, { recursive: true }) as string[]).filter(
    (p) => p.endsWith("/web/playground.json") && !p.includes("node_modules") && !p.includes("/dist/"),
  );
  expect(manifests.length).toBeGreaterThan(0);
  expect(examples.length).toBe(manifests.length);
});

test("the landing page has a card for every example, and each card opens its page", async ({ page }) => {
  await page.goto("/try/");
  const cards = page.locator(".card");
  await expect(cards).toHaveCount(examples.length);
  for (const name of examples) {
    const card = page.locator(`.card[data-example="${name}"]`);
    await expect(card.locator("h2")).toHaveText(manifest(name).title);
  }
  await page.locator('.card[data-example="getting-started"] a.open').click();
  await expect(page).toHaveURL(/\/try\/getting-started\//);
  await expect(page.locator("#status")).toContainText("Connected to", { timeout: 30_000 });
});

test("a playground opens on its manifest's first wire when the URL names none", async ({ page }) => {
  const name = examples[0];
  const first = wiresOf(name)[0];
  await page.goto(`/try/${name}/`);
  await expect(page.locator("#status")).toContainText(`${first} wire`, { timeout: 30_000 });
  await expect(page).toHaveURL(new RegExp(`wire=${first}`));
});

async function openTool(page: Page, smoke: Smoke): Promise<Locator> {
  if (smoke.group) {
    await page.locator("#tab-groups").click();
    const group = page.locator(`#groups > .group[data-group="${smoke.group}"]`);
    if ((await group.getAttribute("open")) === null) await group.locator(":scope > summary").click();
  }
  const tool = page.locator(`.tool[data-tool="${smoke.tool}"]`).first();
  await expect(tool).toBeAttached();
  const summary = tool.locator(":scope > summary");
  if ((await summary.count()) && (await tool.getAttribute("open")) === null) await summary.click();
  return tool;
}

for (const name of examples) {
  for (const wire of wiresOf(name)) {
    test(`${name} on the ${wire} wire: connects and answers a call`, async ({ page }) => {
      const errors: string[] = [];
      page.on("pageerror", (e) => errors.push(e.message));
      page.on("console", (m) => m.type() === "error" && errors.push(m.text()));

      await page.goto(`/try/${name}/?wire=${wire}`);
      await expect(page.locator("#status")).toContainText(`${wire} wire`, { timeout: 30_000 });
      await expect(page.locator("#title")).toHaveText(manifest(name).title);
      await expect(page.locator("#about li")).toHaveText(manifest(name).about);

      const log = page.locator("#log");
      if (wire === "stateless") {
        // server/discover replaces initialize, and every request carries the
        // SEP-2575 envelope.
        await expect(log).toContainText('"method": "server/discover"');
        await expect(log).toContainText('"io.modelcontextprotocol/protocolVersion": "2026-07-28"');
        await expect(log).not.toContainText('"method": "initialize"');
      } else {
        await expect(log).toContainText('"method": "initialize"');
        await expect(log).not.toContainText('"method": "server/discover"');
      }

      // A raw request sent without an id gets the page's next one, and the
      // response comes back under the same id.
      await page.locator("summary", { hasText: "Send raw JSON-RPC" }).click();
      const raw = JSON.parse(await page.locator("#raw").inputValue());
      delete raw.id;
      await page.locator("#raw").fill(JSON.stringify(raw));
      await page.locator("#sendRaw").click();
      const sent = await page.locator("#log .msg.out .head").last().textContent();
      const id = sent?.match(new RegExp(`^→ request #(\\d+) ${raw.method}$`))?.[1];
      expect(id, `raw request header: ${sent}`).toBeTruthy();
      await expect(page.locator("#log .msg.in .head", { hasText: `← response #${id} ${raw.method}` })).toHaveCount(1);
      await page.locator("summary", { hasText: "Send raw JSON-RPC" }).click();

      const smoke = manifest(name).smoke;
      if (!smoke) {
        await expect(page.locator(".tool, .item, .group").first()).toBeAttached();
        expect(errors).toEqual([]);
        return;
      }

      const nested = manifest(name).nested;
      if (nested) {
        await page.locator("#tab-groups").click();
        for (const parent of nested.parents) {
          const p = page.locator(`#groups > .group[data-group="${parent}"]`);
          await p.locator(":scope > summary").click();
          const child = p.locator(`.group[data-group="${nested.child}"]`);
          await child.locator(":scope > summary").click();
          await expect(child).toContainText(nested.expect, { timeout: 15_000 });
        }
        await expect(page.locator(`#groups > .group[data-group="${nested.child}"]`)).toHaveCount(0);
      }

      const tool = await openTool(page, smoke);
      for (const [field, value] of Object.entries(smoke.args ?? {})) {
        await tool.locator(`[name="${field}"]`).fill(value);
      }
      for (const [field, f] of Object.entries(smoke.files ?? {})) {
        await tool.locator(`[name="${field}"]`).setInputFiles({ name: f.name, mimeType: f.mimeType, buffer: Buffer.from(f.text) });
      }
      await tool.getByRole("button", { name: /^Call / }).click();
      for (const label of smoke.replies ?? []) {
        await tool.getByRole("button", { name: label }).click();
      }
      await expect(tool.locator(".result").first()).toContainText(smoke.expect, { timeout: 15_000 });
      await expect(log).toContainText('"method": "tools/call"');

      const complete = manifest(name).complete;
      if (complete) {
        await page.locator('.tabs button[data-pane="prompts"]').click();
        const prompt = page.locator("#prompts .item").filter({ has: page.locator("summary b", { hasText: complete.prompt }) });
        await prompt.locator(":scope > summary").click();
        await prompt.locator(`input[name="${complete.argument}"]`).fill(complete.value);
        await expect(prompt.locator(`datalist option[value="${complete.expect}"]`)).toBeAttached({ timeout: 5_000 });
        await expect(log).toContainText('"method": "completion/complete"');
      }
      expect(errors).toEqual([]);
    });
  }
}
