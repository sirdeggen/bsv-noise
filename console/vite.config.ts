import { defineConfig } from "vite";
import { viteSingleFile } from "vite-plugin-singlefile";

export default defineConfig({
  plugins: [viteSingleFile()],
  build: {
    assetsInlineLimit: 100000000,
    cssCodeSplit: false,
    target: "es2022",
  },
  server: {
    // `npm run dev` talks to the local Go daemon for /api and /healthz.
    proxy: {
      "/api": "http://127.0.0.1:8741",
      "/healthz": "http://127.0.0.1:8741",
    },
  },
});
