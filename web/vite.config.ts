import path from "node:path";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// `pnpm dev` serves the UI and sends API calls to a local inaba-server.
const api = { target: "http://localhost:8080", changeOrigin: false };

export default defineConfig({
  // inaba-server serves the UI under /ui/, beside the API at /v1/.
  base: "/ui/",
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": path.resolve(import.meta.dirname, "./src") } },
  // inaba-server embeds this directory.
  build: {
    outDir: "../internal/webui/dist",
    emptyOutDir: true,
    // The app, and the API reference as a page of its own.
    rollupOptions: { input: { main: path.resolve(import.meta.dirname, "index.html"), docs: path.resolve(import.meta.dirname, "docs.html") } },
  },
  server: { proxy: { "/v1": api, "/hosts": api, "/auth": api, "/openapi.yaml": api, "/docs": api, "^/inaba\\.v1\\..*": api } },
});
