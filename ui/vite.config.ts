import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwind from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwind()],
  build: { outDir: "../internal/hub/web/dist", emptyOutDir: true },
  server: { proxy: { "/api": "http://127.0.0.1:8080", "/login": "http://127.0.0.1:8080" } },
  test: { environment: "jsdom", setupFiles: ["./src/test-setup.ts"], globals: true },
});
