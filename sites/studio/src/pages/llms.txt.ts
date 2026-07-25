import { byId, llmsTxt } from "@bykami/content";
import type { APIRoute } from "astro";

export const GET: APIRoute = () =>
  new Response(llmsTxt(byId("studio")), {
    headers: { "Content-Type": "text/plain; charset=utf-8" },
  });
