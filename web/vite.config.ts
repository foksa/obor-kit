/// <reference types="vitest/config" />
// obor web's page (T-760): `vite build` writes it into internal/web/dist, which the obor binary embeds; `vite` (make
// web-dev) serves it with hot reload on 127.0.0.1:5173 and proxies /api to a running obor web.
import { svelte } from "@sveltejs/vite-plugin-svelte";
import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { defineConfig, type Plugin } from "vite";
import { sourceHash } from "./stamp.ts";

const root = dirname(fileURLToPath(import.meta.url));
const outDir = resolve(root, "../internal/web/dist");

// The dev server: localhost only, a fixed port (its own origin is what the proxy trusts).
const DEV_HOST = "127.0.0.1";
const DEV_PORT = 5173;
const devOrigins = new Set([`http://${DEV_HOST}:${DEV_PORT}`, `http://localhost:${DEV_PORT}`]);
// The obor web the dev server proxies to (OBOR_WEB overrides it).
const target = process.env.OBOR_WEB || "http://127.0.0.1:8787";

// stamp writes source.sha256 next to the bundle (stamp.ts; internal/web's TestDistFresh checks it).
function stamp(): Plugin {
  return {
    name: "obor-source-stamp",
    apply: "build",
    closeBundle() {
      writeFileSync(resolve(outDir, "source.sha256"), sourceHash(root) + "\n");
    },
  };
}

export default defineConfig({
  plugins: [svelte(), stamp()],
  build: {
    outDir,
    emptyOutDir: true,
    assetsDir: "assets",
    chunkSizeWarningLimit: 1024,
  },
  server: {
    host: DEV_HOST,
    port: DEV_PORT,
    strictPort: true,
    proxy: {
      "/api": {
        target,
        changeOrigin: false,
        // obor web takes a POST only from its own page (Origin http://<its address>, Host its address). A request from
        // this dev server's own page is that page: its Origin and Host become obor web's. Any other request goes on
        // as it came, so a page on another site that posts to localhost:5173 is refused by obor web as before.
        configure(proxy) {
          proxy.on("proxyReq", (proxyReq, req) => {
            const origin = req.headers.origin;
            if (typeof origin === "string" && devOrigins.has(origin)) {
              const t = new URL(target);
              proxyReq.setHeader("origin", t.origin);
              proxyReq.setHeader("host", t.host);
            }
          });
        },
      },
    },
  },
  test: {
    include: ["src/**/*.test.ts"],
    environment: "node",
  },
});
