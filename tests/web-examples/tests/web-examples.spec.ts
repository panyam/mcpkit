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
  replies?: string[]; // buttons to press in order after the call, e.g. "Send responses"
  expect: string; // text the result must contain
};
type Wire = "stateless" | "session";
type Manifest = { title: string; summary: string; docs?: string; package?: string; wires?: Wire[]; smoke?: Smoke };

const examples = readdirSync(new URL("../.site/try/", import.meta.url), { withFileTypes: true })
  .filter((d) => d.isDirectory())
  .map((d) => d.name);
const manifest = (name: string): Manifest =>
  JSON.parse(readFileSync(new URL(`../.site/try/${name}/playground.json`, import.meta.url), "utf8"));
const wiresOf = (name: string): Wire[] => manifest(name).wires ?? ["stateless", "session"];

test("at least one example page was collected", () => {
  expect(examples.length).toBeGreaterThan(0);
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
    await page.locator(`.group[data-group="${smoke.group}"] > summary`).click();
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

      const smoke = manifest(name).smoke;
      if (!smoke) {
        await expect(page.locator(".tool, .item, .group").first()).toBeAttached();
        expect(errors).toEqual([]);
        return;
      }

      const tool = await openTool(page, smoke);
      for (const [field, value] of Object.entries(smoke.args ?? {})) {
        await tool.locator(`[name="${field}"]`).fill(value);
      }
      await tool.getByRole("button", { name: /^Call / }).click();
      for (const label of smoke.replies ?? []) {
        await tool.getByRole("button", { name: label }).click();
      }
      await expect(tool.locator(".result").first()).toContainText(smoke.expect, { timeout: 15_000 });
      await expect(log).toContainText('"method": "tools/call"');
      expect(errors).toEqual([]);
    });
  }
}
