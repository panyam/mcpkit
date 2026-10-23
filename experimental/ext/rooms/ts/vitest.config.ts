import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "node",
    // The first test file compiles the Go teststack.
    hookTimeout: 180_000,
    testTimeout: 20_000,
    fileParallelism: false,
  },
});
