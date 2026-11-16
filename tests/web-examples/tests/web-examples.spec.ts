import { test, expect, type Page, type Locator } from "@playwright/test";
import { readdirSync } from "node:fs";

// One test per page that scripts/collect_web_examples.sh produced, so a new
// browser-ready example is loaded and connected with no edit here.

type Smoke = {
  tool: string;
  group?: string; // open this group first, for servers that list the tool only under groups/get
  args?: Record<string, string>;
  replies?: string[]; // buttons to press in order after the call, e.g. "Send responses"
  expect: string; // text the result must contain
};

// A known call per example. An example missing here still has to connect
// cleanly and list at least one tool, resource or group.
const smokes: Record<string, Smoke> = {
  "getting-started": { tool: "greet", args: { name: "world" }, expect: "Hello, world!" },
  "tasks-v2": { tool: "confirm_delete", args: { filename: "a.txt" }, replies: ["Send to task"], expect: "deleted 'a.txt'" },
  mrtr: {
    tool: "test_incomplete_result_multi_round",
    replies: ["Send responses", "Send responses"],
    expect: "your favorite color is",
  },
  "list-ttl": { tool: "ping", expect: "pong" },
  "skills-core": { tool: "format_commit", args: { type: "feat", summary: "add the playground" }, expect: "feat: add the playground" },
  disclosure: { tool: "billing_invoice_list", group: "billing", expect: "billing_invoice_list: ok" },
};

const examples = readdirSync(new URL("../.site/try/", import.meta.url), { withFileTypes: true })
  .filter((d) => d.isDirectory())
  .map((d) => d.name);

test("at least one example page was collected", () => {
  expect(examples.length).toBeGreaterThan(0);
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
  test(`${name}: loads, connects, and answers a call`, async ({ page }) => {
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    page.on("console", (m) => m.type() === "error" && errors.push(m.text()));

    await page.goto(`/try/${name}/`);
    await expect(page.locator("#status")).toContainText("Connected to", { timeout: 30_000 });

    const smoke = smokes[name];
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

    // The wire log shows the call and its answer.
    await expect(page.locator("#log")).toContainText('"method": "tools/call"');
    expect(errors).toEqual([]);
  });
}
