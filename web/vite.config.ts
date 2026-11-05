import { defineConfig } from "vite";

export default defineConfig({
  build: { chunkSizeWarningLimit: 6000 },
  server: { proxy: { "/api": { target: "http://127.0.0.1:8090", ws: true } } },
});
