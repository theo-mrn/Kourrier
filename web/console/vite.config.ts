import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// The build is embedded in the Go binary (internal/console, go:embed).
const outDir = fileURLToPath(new URL("../../internal/console/dist", import.meta.url));

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    {
      // emptyOutDir removes the placeholder that keeps the directory in Git.
      name: "keep-dist-placeholder",
      closeBundle() {
        writeFileSync(`${outDir}/.gitkeep`, "");
      },
    },
  ],
  build: {
    outDir,
    emptyOutDir: true,
    // The console's CSP only allows fonts and scripts from 'self': no
    // asset is inlined as a data: URI.
    assetsInlineLimit: 0,
  },
  server: {
    // make console-dev: the admin API of a local Kourrier (make up).
    proxy: { "/api": process.env.KOURRIER_CONSOLE_URL ?? "http://localhost:8181" },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
  },
});
