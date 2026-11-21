import { defineConfig } from "@playwright/test";

const PORT = 18790;

export default defineConfig({
  testDir: "tests",
  timeout: 60_000,
  use: { baseURL: `http://localhost:${PORT}` },
  webServer: {
    // The Go example itself, which serves /mcp and the /host page.
    command: `go run . -addr :${PORT}`,
    cwd: "..",
    url: `http://localhost:${PORT}/host`,
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
