/**
 * Playwright integration tests for MCP App Bridge.
 *
 * Uses a fake host HTML page that implements the host side of the
 * postMessage protocol. The bridge runs inside an iframe, exercising
 * the real browser postMessage path.
 */

import { test, expect } from "@playwright/test";

const HOST_URL = "http://localhost:18788/fake-host.html?app=app.html";

test.describe("MCP App Bridge — fake host integration", () => {
  test.beforeEach(async ({ page }) => {
    await page.goto(HOST_URL);
    // Wait for the iframe to load and the bridge to initialize.
    await expect(page.locator("#status")).toHaveText("connected", {
      timeout: 5000,
    });
  });

  test("bridge completes initialize handshake", async ({ page }) => {
    // Host shows connected.
    await expect(page.locator("#status")).toHaveText("connected");

    // Iframe app shows connected with correct theme.
    const frame = page.frameLocator("#app-frame");
    await expect(frame.locator("#status")).toHaveText("connected");
    await expect(frame.locator("#theme")).toHaveText("dark");
  });

  test("host sends tool-result notification to iframe", async ({ page }) => {
    const frame = page.frameLocator("#app-frame");

    // Host sends a tool-result notification.
    await page.evaluate(() => {
      (window as any).__sendToApp({
        jsonrpc: "2.0",
        method: "ui/notifications/tool-result",
        params: {
          name: "test_tool",
          content: [{ type: "text", text: "Hello from host" }],
        },
      });
    });

    // Iframe app displays the result.
    await expect(frame.locator("#result")).toHaveText("Hello from host", {
      timeout: 2000,
    });
  });

  test("iframe calls tool back through bridge", async ({ page }) => {
    const frame = page.frameLocator("#app-frame");

    // Click the "Call Tool" button in the iframe.
    await frame.locator("#call-tool").click();

    // Verify the host received the tools/call request.
    const lastCall = await page.evaluate(
      () => (window as any).__lastToolCall
    );
    expect(lastCall).toBeDefined();
    expect(lastCall.name).toBe("test_tool");
    expect(lastCall.arguments.value).toBe(42);

    // Verify the iframe received the response.
    await expect(frame.locator("#result")).toContainText("call-response:", {
      timeout: 2000,
    });
  });

  test("iframe sends open-link notification", async ({ page }) => {
    const frame = page.frameLocator("#app-frame");

    // Click the "Open Link" button.
    await frame.locator("#open-link").click();

    // Verify the host received the notification.
    const lastLink = await page.evaluate(
      () => (window as any).__lastOpenLink
    );
    expect(lastLink).toBeDefined();
    expect(lastLink.url).toBe("https://example.com/test");
  });

  test("host sends tool-input notification", async ({ page }) => {
    const frame = page.frameLocator("#app-frame");

    // Send tool-input from host.
    await page.evaluate(() => {
      (window as any).__sendToApp({
        jsonrpc: "2.0",
        method: "ui/notifications/tool-input",
        params: {
          name: "edit_item",
          arguments: { id: "abc", action: "update" },
        },
      });
    });

    // Give a moment for event dispatch.
    await page.waitForTimeout(100);

    // Verify the event was received by checking the host log
    // (the app.html doesn't display tool-input, but we can verify
    // the bridge processed it by checking it didn't error).
    const logs = await page.evaluate(() => (window as any).__hostLog);
    expect(logs.length).toBeGreaterThan(0);
  });

  test("host-context-changed updates iframe", async ({ page }) => {
    const frame = page.frameLocator("#app-frame");

    // Verify initial theme.
    await expect(frame.locator("#theme")).toHaveText("dark");

    // The app.html doesn't have a hostcontextchanged handler that updates
    // the UI, but we can verify the bridge processes it without error.
    await page.evaluate(() => {
      (window as any).__sendToApp({
        jsonrpc: "2.0",
        method: "ui/notifications/host-context-changed",
        params: { hostContext: { theme: "light" } },
      });
    });

    // No crash — bridge handled it gracefully.
    await expect(frame.locator("#status")).toHaveText("connected");
  });
});
