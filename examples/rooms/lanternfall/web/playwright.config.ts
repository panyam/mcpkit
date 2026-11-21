import { defineConfig } from "@playwright/test";

export const PORT = 18791;

export default defineConfig({
  testDir: "tests",
  timeout: 90_000,
  use: { baseURL: `http://localhost:${PORT}` },
  // The checks share one server, and each uses a game of its own.
  workers: 1,
  reporter: [["list", { printSteps: true }]],
  webServer: {
    // The Go example itself: /mcp, the relay, the join endpoint and the page.
    command: `go run . -addr :${PORT}`,
    cwd: "..",
    url: `http://localhost:${PORT}/whoami`,
    reuseExistingServer: !process.env.CI,
    timeout: 180_000,
  },
  // CI sets CHROME_PATH to the runner's Chrome (#1742). Unset, Playwright
  // uses its own Chromium.
  projects: [
    {
      name: "chromium",
      use: {
        browserName: "chromium",
        launchOptions: { executablePath: process.env.CHROME_PATH || undefined },
      },
    },
  ],
});
