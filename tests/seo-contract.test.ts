import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { beforeAll, describe, expect, it } from "vitest";
import { valueOf, verticals, type Vertical } from "@bykami/content";

/**
 * The SEO contract, asserted against built output.
 *
 * One seam, deliberately. Every clause below is a property of the HTML actually
 * served, not of any component that produced it — a test proving a pricing
 * component renders a table cell proves nothing about whether the price reached
 * the built page. Components stay free to change; this suite does not.
 *
 * Requires `pnpm -r build` first.
 */
const ROOT = new URL("..", import.meta.url).pathname;
const distOf = (id: string) => join(ROOT, "sites", id, "dist");

const read = (id: string, file: string) => readFileSync(join(distOf(id), file), "utf8");

const jsonLdBlocks = (html: string): Record<string, unknown>[] =>
  [...html.matchAll(/<script type="application\/ld\+json">(.*?)<\/script>/gs)].map((m) =>
    JSON.parse(m[1]!),
  );

/**
 * Collapses all whitespace, including the non-breaking space `Intl` puts between
 * "Rp" and the figure. Both sides of every assertion go through this, so a match
 * means the crawler reads the same characters regardless of which space is used.
 */
const norm = (s: string) => s.replace(/ /g, " ").replace(/\s+/g, " ");

/** Strips tags so assertions test what a crawler reads, not raw markup. */
const textOf = (html: string) =>
  norm(
    html
      .replace(/<script[\s\S]*?<\/script>/g, " ")
      .replace(/<style[\s\S]*?<\/style>/g, " ")
      .replace(/<[^>]+>/g, " ")
      .replace(/&nbsp;/g, " "),
  );

const idr = new Intl.NumberFormat("id-ID", {
  style: "currency",
  currency: "IDR",
  maximumFractionDigits: 0,
});

beforeAll(() => {
  for (const v of verticals) {
    if (!existsSync(join(distOf(v.id), "index.html"))) {
      throw new Error(`No build for ${v.id}. Run \`pnpm -r build\` first.`);
    }
  }
});

describe.each(verticals.map((v) => [v.id, v] as const))("%s", (id, vertical: Vertical) => {
  let html: string;
  let text: string;

  beforeAll(() => {
    html = read(id, "index.html");
    text = textOf(html);
  });

  it("serves every price as crawlable text, with no JavaScript", () => {
    for (const o of vertical.offerings) {
      const price = valueOf(o.priceIDR);
      if (price === undefined) continue;
      expect(text, `${o.id} price missing from served HTML`).toContain(norm(idr.format(price)));
    }
  });

  it("names every offering in the served HTML", () => {
    for (const o of vertical.offerings) {
      expect(text, `${o.id} name missing`).toContain(o.name);
    }
  });

  it("emits only valid JSON-LD", () => {
    const blocks = jsonLdBlocks(html);
    expect(blocks.length).toBeGreaterThan(0);
    for (const b of blocks) expect(b["@context"]).toBe("https://schema.org");
  });

  it("declares one Organization tied to the platform root", () => {
    const org = jsonLdBlocks(html).find((b) => b["@type"] === "Organization");
    expect(org).toBeDefined();
    if (vertical.id === "root") {
      expect(org!["parentOrganization"]).toBeUndefined();
    } else {
      expect(org!["parentOrganization"]).toEqual({ "@id": "https://bykami.id/#organization" });
    }
  });

  it("emits no Offer while prices are unverified", () => {
    expect(html).not.toContain('"@type":"Offer"');
  });

  /**
   * Emitted exactly when the address is owner-confirmed, rather than never.
   *
   * Written as a conjunction so that confirming an address is a content change
   * and not a test change: a hardcoded `false` here would have to be edited by
   * whoever verifies the next one, which makes the suite something to work
   * around rather than the thing that catches a fact escaping the gate.
   */
  it("emits a LocalBusiness subtype exactly when its address is verified", () => {
    const confirmed = vertical.nap?.address.status === "verified";
    const blocks = jsonLdBlocks(html);
    const emitted =
      blocks.some((b) => b["@type"] === "PhotographyBusiness") ||
      blocks.some((b) => b["@type"] === "Restaurant");
    expect(emitted).toBe(confirmed && vertical.schemaType !== "Organization");
  });

  it("points its canonical at the final hostname, never the preview host", () => {
    expect(html).toContain(`rel="canonical" href="https://${vertical.hostname}/"`);
    expect(html).not.toContain("pages.dev");
  });

  /**
   * Indexing is the conjunction of the environment gate the build ran under and
   * the vertical's own editorial flag, so this reads the same two inputs the
   * build did rather than assuming an answer. CI's verify job builds with the
   * environment gate open, which is what puts both branches below under test in
   * every run: three properties take the indexable path, dimsamcong the other.
   */
  const shouldIndex = process.env["BYKAMI_INDEXABLE"] === "true" && vertical.indexable;

  it(`is ${shouldIndex ? "indexable" : "held out of the index"}`, () => {
    const robots = read(id, "robots.txt");
    // robots.txt stops a fetch; it does not stop an externally-linked URL from
    // being indexed. X-Robots-Tag does, and unlike a meta tag it also applies to
    // llms.txt and the sitemaps — so all three have to agree.
    if (shouldIndex) {
      expect(html).not.toContain('name="robots" content="noindex');
      expect(robots).toContain("Allow: /");
      expect(robots).not.toContain("Disallow: /");
      expect(read(id, "_headers")).not.toContain("X-Robots-Tag");
    } else {
      expect(html).toContain('name="robots" content="noindex, nofollow"');
      expect(robots).toContain("Disallow: /");
      expect(read(id, "_headers")).toContain("X-Robots-Tag: noindex, nofollow");
    }
  });

  it("names the AI crawlers explicitly once it is indexable", () => {
    if (!shouldIndex) return;
    // The catalogue exists to be quoted by answer engines. Naming each crawler
    // rather than relying on `User-agent: *` is what makes that a grant instead
    // of an accident.
    for (const ua of ["GPTBot", "ClaudeBot", "PerplexityBot", "CCBot", "Google-Extended"]) {
      expect(read(id, "robots.txt"), `${ua} not named`).toContain(`User-agent: ${ua}`);
    }
  });

  it("links to every other property", () => {
    for (const other of verticals.filter((v) => v.id !== id)) {
      expect(html, `missing link to ${other.hostname}`).toContain(`https://${other.hostname}`);
    }
  });

  it("publishes a sitemap", () => {
    expect(read(id, "sitemap-index.xml")).toContain(vertical.hostname);
  });

  it("serves llms.txt naming the business", () => {
    expect(read(id, "llms.txt")).toContain(vertical.displayName);
  });

  it("leaks no placeholder marker, TODO, or unresolved template", () => {
    expect(text).not.toMatch(/\bTODO\b|\bFIXME\b|\bPLACEHOLDER\b|\bLorem ipsum\b/i);
    expect(text).not.toMatch(/\{\{|\}\}|\[object Object\]|undefined,|,undefined/);
  });

  it("renders nothing for sections whose data is absent", () => {
    if (vertical.offerings.length === 0) {
      expect(text).not.toContain("Paket & harga");
      expect(text).not.toContain("Menu");
    }
    const anyAnswered = vertical.faqs.some((f) => valueOf(f.answer) !== undefined);
    if (!anyAnswered) expect(text).not.toContain("Pertanyaan umum");
  });

  it("declares Indonesian", () => {
    expect(html).toContain('lang="id"');
  });
});

describe("cross-property", () => {
  it("gives every vertical a distinct hostname", () => {
    const hosts = verticals.map((v) => v.hostname);
    expect(new Set(hosts).size).toBe(hosts.length);
  });

  it("shares no page copy between properties", () => {
    const leads = verticals.map((v) => textOf(read(v.id, "index.html")).slice(0, 400));
    expect(new Set(leads).size).toBe(leads.length);
  });
});
