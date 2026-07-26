import { writeFile } from "node:fs/promises";

/**
 * Emits a Cloudflare Pages `_headers` file at the end of the build.
 *
 * `robots.txt` stops compliant crawlers from *fetching*, but a URL that is
 * linked from anywhere external can still be indexed without ever being
 * fetched. `X-Robots-Tag: noindex` is the signal that actually prevents that,
 * and unlike a meta tag it also covers `llms.txt`, `sitemap-index.xml`, and
 * every other non-HTML response.
 *
 * That matters here specifically: an indexed *.pages.dev copy would compete
 * with bykami.id for the same queries the moment the real domain launches.
 *
 * Indexing stays opt-in — this writes the noindex header unless
 * BYKAMI_INDEXABLE is explicitly "true".
 */
export const pagesHeaders = () => ({
  name: "bykami:pages-headers",
  hooks: {
    "astro:build:done": async ({ dir, logger }) => {
      const indexable = process.env.BYKAMI_INDEXABLE === "true";

      const lines = indexable
        ? [
            "/*",
            "  X-Content-Type-Options: nosniff",
            "  Referrer-Policy: strict-origin-when-cross-origin",
            "",
          ]
        : [
            "/*",
            "  X-Robots-Tag: noindex, nofollow",
            "  X-Content-Type-Options: nosniff",
            "  Referrer-Policy: strict-origin-when-cross-origin",
            "",
          ];

      await writeFile(new URL("_headers", dir), lines.join("\n"), "utf8");
      logger.info(indexable ? "_headers written (indexable)" : "_headers written (noindex)");
    },
  },
});
