import { defineConfig } from "vitest/config";

// Unit tests for the frontend's pure modules (src/*.test.ts). Kept separate
// from vite.config.ts so the Wails bindings plugin isn't involved.
export default defineConfig({
  test: {
    include: ["src/**/*.test.ts"],
    environment: "node",
  },
});
