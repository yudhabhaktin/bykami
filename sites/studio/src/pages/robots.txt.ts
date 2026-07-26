import type { APIRoute } from "astro";

/**
 * Indexing is opt-in. While the sites live on *.pages.dev this file disallows
 * everything; forgetting the flag keeps them out of the index rather than
 * letting them in.
 *
 * Once BYKAMI_INDEXABLE is set, AI crawlers are named and allowed explicitly.
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
  const indexable = import.meta.env["BYKAMI_INDEXABLE"] === "true";

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
