import { defineConfig } from "@playwright/test";

const PORT = 18791;

// scripts/test-web-examples.sh fills .site/try/<example>/ with the same files
// the docs site publishes, so the pages load from the same paths here.
export default defineConfig({
  testDir: "tests",
  timeout: 60_000,
  use: { baseURL: `http://127.0.0.1:${PORT}` },
  webServer: {
    command: `python3 -m http.server ${PORT} --bind 127.0.0.1 -d .site`,
    url: `http://127.0.0.1:${PORT}/try/`,
    reuseExistingServer: false,
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
