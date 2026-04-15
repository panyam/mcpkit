import { defineConfig } from "@playwright/test";

const PORT = 18788;

export default defineConfig({
  testDir: ".",
  testMatch: "*.spec.ts",
  timeout: 15000,
  use: {
    baseURL: `http://localhost:${PORT}`,
  },
  webServer: {
    // Serve the test HTML files + bridge JS via a simple static server.
    command: `npx serve -l ${PORT} . --no-clipboard`,
    port: PORT,
    reuseExistingServer: !process.env.CI,
    timeout: 10000,
  },
  projects: [
    {
      name: "chromium",
      use: { browserName: "chromium" },
    },
  ],
});
