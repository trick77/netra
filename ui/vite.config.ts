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
  // Built straight into the Go embed directory, which also holds a tracked
  // .gitkeep so `//go:embed all:dist` always has something to embed.
  // emptyOutDir:true would delete it; ship check (ui.embed) and make fe-build clear the rest.
  build: { outDir: "../backend/internal/hub/web/dist", emptyOutDir: false },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8080",
      "/login": "http://127.0.0.1:8080",
      // The rail's Sign out posts here. Without the entry the dev server
      // answers it itself, and its SPA fallback only serves GET, so the post
      // 404s instead of clearing the session.
      "/logout": "http://127.0.0.1:8080",
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    // Dates render in the operator's zone; the expectations are written for
    // Zurich, so the suite pins it rather than following the machine's.
    env: { TZ: "Europe/Zurich" },
    coverage: {
      provider: "v8",
      // lcov feeds ship's coverage gates (ship.yaml ui.lcov); text-summary is
      // for the human reading the log.
      reporter: ["text-summary", "lcov"],
      reportsDirectory: "../coverage/ui",
      include: ["src/**/*.{ts,tsx}"],
      // main.tsx is three lines of bootstrap that mount the app into a real
      // DOM, and src/test/ is the harness itself.
      exclude: ["src/main.tsx", "src/test/**", "src/**/*.test.{ts,tsx}"],
    },
  },
});
