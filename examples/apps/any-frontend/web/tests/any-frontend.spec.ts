// Drives examples/apps/any-frontend through its /host page, which renders
// each View with upstream's reference AppBridge against the Go server.
// Every View must render the tool result, call the server back, and push
// model context, whichever runtime it is built on.
import { expect, test, type Page } from "@playwright/test";

const VIEWS = [
  { tool: "pick_color_bridge", runtime: "mcpkit bridge" },
  { tool: "pick_color_vanilla", runtime: "upstream App" },
  { tool: "pick_color_react", runtime: "upstream React useApp" },
  { tool: "pick_color_extras", runtime: "upstream App + mcpkit extras" },
];

// Mirrors darker() in main.go.
function darker(hex: string): string {
  const v = parseInt(hex.slice(1), 16);
  const ch = (n: number) => Math.floor((n * 8) / 10).toString(16).padStart(2, "0");
  return `#${ch((v >> 16) & 0xff)}${ch((v >> 8) & 0xff)}${ch(v & 0xff)}`;
}

async function hostState(page: Page) {
  return page.evaluate(() => (window as any).__anyFrontend);
}

test.beforeEach(async ({ page }) => {
  await page.goto("/host");
  await page.waitForFunction(() => (window as any).__anyFrontend?.ready === true);
  const state = await hostState(page);
  expect(state.errors).toEqual([]);
});

for (const { tool, runtime } of VIEWS) {
  test(`${tool}: renders, calls the server, pushes model context`, async ({ page }) => {
    const view = page.frameLocator(`iframe[data-tool="${tool}"]`);
    await expect(view.getByTestId("runtime")).toHaveText(runtime);

    // 1. The tool result reaches the View.
    const swatch = view.getByTestId("swatch");
    await expect(swatch).toHaveAttribute("data-hex", /^#[0-9a-f]{6}$/);
    const first = (await swatch.getAttribute("data-hex"))!;

    // 2. The View pushed model context for it.
    await expect.poll(async () => (await hostState(page)).views[tool].lastContext?.structuredContent?.hex).toBe(first);

    // 3. The View calls a server tool through the host, and shows the answer.
    await view.getByTestId("darker").click();
    await expect(swatch).toHaveAttribute("data-hex", darker(first));
    await expect(view.getByTestId("status")).toHaveText("shaded by the server");
    await expect.poll(async () => (await hostState(page)).views[tool].lastContext?.structuredContent?.hex).toBe(darker(first));
  });
}

test("the extras View stamps traceparent on its tools/call and plain upstream App does not", async ({ page }) => {
  for (const tool of ["pick_color_extras", "pick_color_vanilla"]) {
    await page.frameLocator(`iframe[data-tool="${tool}"]`).getByTestId("swatch").waitFor();
    await page.frameLocator(`iframe[data-tool="${tool}"]`).getByTestId("darker").click();
  }
  await expect.poll(async () => (await hostState(page)).views.pick_color_extras.toolCallMeta.length).toBeGreaterThan(0);
  const state = await hostState(page);
  expect(state.views.pick_color_extras.toolCallMeta[0]?.traceparent).toMatch(/^00-[0-9a-f]{32}-[0-9a-f]{16}-01$/);
  expect(state.views.pick_color_vanilla.toolCallMeta[0]?.traceparent).toBeUndefined();
});
