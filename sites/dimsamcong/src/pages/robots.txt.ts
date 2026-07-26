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
 * Note this file alone is not sufficient. Cloudflare blocks known AI crawlers at
 * the edge by default on zones created since July 2025, before the request ever
 * reaches origin — so a permissive robots.txt is never read. The zone setting
 * must be changed too, and verified by fetching a deployed URL with a ClaudeBot
 * or GPTBot user-agent.
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
    import.meta.env["BYKAMI_INDEXABLE"] === "true" && byId("dimsamcong").indexable;

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
