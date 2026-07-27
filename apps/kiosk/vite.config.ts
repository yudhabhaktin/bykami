import { writeFileSync } from "node:fs";
import { resolve } from "node:path";

import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

const OUT_DIR = "../../agent/internal/httpd/dist";

/*
 * The output directory is embedded by the agent, so it has to exist on a clean
 * checkout or `go build` fails on a missing embed pattern — which would mean
 * the Go module could not be built without a Node toolchain.
 *
 * The built assets are gitignored; a committed .gitkeep is what keeps the
 * directory in the tree. emptyOutDir deletes it on every build, so it is put
 * back afterwards rather than trading away build hygiene to preserve it.
 */
function keepEmbedMarker(): Plugin {
  return {
    name: "bykami-keep-embed-marker",
    closeBundle() {
      writeFileSync(resolve(__dirname, OUT_DIR, ".gitkeep"), "");
    },
  };
}

/*
 * Builds straight into the agent, which then embeds the directory with
 * embed.FS. One artifact and one version: a UI change is an agent change, so
 * version skew between the screen and the binary driving the camera is
 * impossible by construction rather than by handshake.
 *
 * The ordering matters and is easy to get wrong — `go build` must run after
 * this, or the binary ships yesterday's UI. CI enforces it.
 */
export default defineConfig({
  plugins: [react(), keepEmbedMarker()],
  build: {
    outDir: OUT_DIR,
    emptyOutDir: true,
    // Relative, because the bundle is served from the agent's root and nothing
    // else. An absolute base would break the moment the server mounts it
    // anywhere but /.
    assetsInlineLimit: 0,
  },
  base: "./",
  server: {
    // `pnpm dev` talks to a running agent instead of the embedded copy, so the
    // UI can be iterated on without a Go build between every change.
    proxy: {
      "/api": "http://127.0.0.1:8899",
    },
  },
});
