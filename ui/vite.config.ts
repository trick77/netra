// defineConfig comes from "vitest/config", not "vite": the `test` key below is
// Vitest's augmentation of Vite's config type, and vite's own defineConfig
// does not know it. Imported from "vite" the block typechecks as excess
// properties nobody validates, so a typo in `environment` or `setupFiles`
// silently yields a different test environment with no diagnostic.
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwind from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwind()],
  build: { outDir: "../internal/hub/web/dist", emptyOutDir: true },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8080",
      "/login": "http://127.0.0.1:8080",
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
    globals: true,
    coverage: {
      // The Go side keeps its floors in hack/coverage-floors and enforces
      // them in coverage-gate.sh; this is the same idea in the tool that
      // measures this half. A hard floor, not a ratchet: each number sits a
      // couple of points under what the suite measures today, which is
      // margin for an honest refactor rather than slack. Raise them the same
      // way the Go ones are raised -- measure first, then set.
      provider: "v8",
      include: ["src/**/*.{ts,tsx}"],
      // main.tsx is three lines of bootstrap that mount the app into a real
      // DOM, and test-setup.ts is the harness itself.
      exclude: ["src/main.tsx", "src/test-setup.ts", "src/**/*.test.{ts,tsx}"],
      thresholds: {
        statements: 89,
        branches: 82,
        functions: 89,
        lines: 90,
      },
    },
  },
});
