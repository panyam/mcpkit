import { test, expect } from "@playwright/test";
import { readdirSync } from "node:fs";

// One test per page that scripts/collect_web_examples.sh produced. Each page
// fills its first tool's form with defaults, so calling it needs no input.
const examples = readdirSync(new URL("../.site/try/", import.meta.url), { withFileTypes: true })
  .filter((d) => d.isDirectory())
  .map((d) => d.name);

test("at least one example page was collected", () => {
  expect(examples.length).toBeGreaterThan(0);
});

const expected: Record<string, string> = {
  "getting-started": "Hello, world!",
};

for (const name of examples) {
  test(`${name}: loads, lists tools, and calls the first one`, async ({ page }) => {
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    page.on("console", (m) => m.type() === "error" && errors.push(m.text()));

    await page.goto(`/try/${name}/`);
    await expect(page.locator("#status")).toContainText("Connected to", { timeout: 30_000 });

    const tool = page.locator(".tool").first();
    await expect(tool).toBeVisible();
    await tool.getByRole("button").click();

    const result = tool.locator(".result");
    await expect(result).not.toBeEmpty();
    await expect(result).not.toContainText("Error");
    if (expected[name]) await expect(result).toHaveText(expected[name]);

    // The wire log shows the tools/call request and its response.
    const log = page.locator("#log");
    await expect(log).toContainText('"method": "tools/call"');
    await expect(log.locator(".msg.in").last()).toContainText('"result"');

    expect(errors).toEqual([]);
  });
}
