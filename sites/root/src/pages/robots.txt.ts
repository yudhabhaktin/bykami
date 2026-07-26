import type { APIRoute } from "astro";
import { byId } from "@bykami/content";

/**
 * Indexing needs two gates open: BYKAMI_INDEXABLE says this build is the real
 * domain rather than a *.pages.dev preview, and the vertical's own `indexable`
 * flag says this property has content worth indexing. Either one closed
 * disallows everything, so forgetting a flag keeps a page out rather than
 * letting it in.
 *
 * Once both are open, AI crawlers are named and allowed explicitly.
 * That is a deliberate content-licensing choice: this catalogue exists to be
 * quoted by answer engines, so granting them access is the entire point.
 *
 * Note this file alone is not sufficient, though not for the reason first
 * assumed. Verified against the live zone on 2026-07-26: ClaudeBot, GPTBot and
 * CCBot all get HTTP 200, so nothing is blocked at the edge. What the zone does
 * do is prepend a Cloudflare-managed block to this file, carrying
 * `Content-Signal: ai-train=no` and an explicit `Disallow: /` for exactly the
 * crawlers named below — so the served file contradicts itself.
 *
 * The fix is a dashboard toggle, not code: Security > Settings > Bot traffic >
 * "Set your preference to block training in robots.txt", plus "Display Content
 * Signals Policy" under Control AI Crawlers on the zone overview. Re-verify by
 * fetching /robots.txt and confirming no "Cloudflare Managed content" block.
 */
const AI_CRAWLERS = [
  "GPTBot",
  "OAI-SearchBot",
  "ChatGPT-User",
  "ClaudeBot",
  "Claude-User",
  "Claude-SearchBot",
  "PerplexityBot",
  "Perplexity-User",
  "CCBot",
  "Google-Extended",
  "Meta-ExternalAgent",
  "Applebot-Extended",
  "Bytespider",
];

export const GET: APIRoute = ({ site }) => {
  const indexable =
    import.meta.env["BYKAMI_INDEXABLE"] === "true" && byId("root").indexable;

  const body = indexable
    ? [
        ...AI_CRAWLERS.map((ua) => `User-agent: ${ua}\nAllow: /\n`),
        "User-agent: *",
        "Allow: /",
        "",
        `Sitemap: ${new URL("sitemap-index.xml", site).href}`,
        "",
      ].join("\n")
    : ["User-agent: *", "Disallow: /", ""].join("\n");

  return new Response(body, {
    headers: { "Content-Type": "text/plain; charset=utf-8" },
  });
};
