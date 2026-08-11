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
  server: { proxy: { "/api": "http://127.0.0.1:8080", "/login": "http://127.0.0.1:8080" } },
  test: { environment: "jsdom", setupFiles: ["./src/test-setup.ts"], globals: true },
});
