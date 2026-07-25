import react from "@astrojs/react";
import sitemap from "@astrojs/sitemap";
import { defineConfig } from "astro/config";

// Canonical URLs and the sitemap both point at the final hostname from the very
// first preview build, so the pages.dev deployment never competes with the real
// domain and cutover is a DNS change rather than a content migration.
export default defineConfig({
  site: "https://studio.bykami.com",
  integrations: [react(), sitemap()],
  build: { inlineStylesheets: "always" },
  compressHTML: true,
});
