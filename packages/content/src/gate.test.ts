import { describe, expect, it } from "vitest";
import { gaps, launchReady } from "./coverage.ts";
import { faqPageLd, localBusinessLd, offerLd, organizationLd } from "./jsonld.ts";
import { postUrl, videoUrl, vertical, type Faq, type Offering, type Vertical } from "./schema.ts";
import { blocked, unverified, verified } from "./sourced.ts";
import { byId, verticals } from "./verticals/index.ts";

const studio = byId("studio");

const offering = (over: Partial<Offering> = {}): Offering => ({
  id: "test",
  name: "Test",
  serviceLine: "self-photo",
  orderIndex: 0,
  priceIDR: unverified(45_000, "test"),
  ...over,
});

const faq = (over: Partial<Faq> = {}): Faq => ({
  id: "q",
  question: "Berapa harganya?",
  topic: "pricing",
  answer: unverified("45 ribu", "test"),
  ...over,
});

describe("the gate blocks unconfirmed facts from structured data", () => {
  it("emits no Offer for an unverified price", () => {
    expect(offerLd(studio, offering())).toBeNull();
  });

  it("emits no Offer for a blocked price", () => {
    expect(offerLd(studio, offering({ priceIDR: blocked("no source") }))).toBeNull();
  });

  it("emits an Offer once the price is owner-verified", () => {
    const ld = offerLd(studio, offering({ priceIDR: verified(45_000, "owner") }));
    expect(ld).not.toBeNull();
    expect(ld).toMatchObject({
      "@type": "Offer",
      price: "45000",
      priceCurrency: "IDR",
    });
  });

  it("emits no FAQPage when no answer is verified", () => {
    expect(faqPageLd(studio, [faq(), faq({ id: "q2", answer: blocked("unknown") })])).toBeNull();
  });

  it("includes only verified answers in FAQPage", () => {
    const ld = faqPageLd(studio, [
      faq({ id: "keep", answer: verified("Buka 09:00–21:00", "owner") }),
      faq({ id: "drop-unverified" }),
      faq({ id: "drop-blocked", answer: blocked("unknown") }),
    ]);
    expect(ld?.["mainEntity"]).toHaveLength(1);
  });
});

describe("LocalBusiness requires a verified address", () => {
  it("emits nothing while the address is only unverified", () => {
    // Deliberately synthetic. This asserts the rule, so it must not depend on
    // whether the real studio address happens to be confirmed yet — otherwise
    // the day an owner confirms one, a test about the gate fails for a reason
    // that has nothing to do with the gate.
    const pending: Vertical = {
      ...studio,
      nap: {
        ...studio.nap!,
        address: unverified(
          {
            streetAddress: "Jalan Yos Sudarso",
            addressLocality: "Jajag",
            addressRegion: "Banyuwangi",
            addressCountry: "ID",
          },
          "a PDF",
        ),
      },
    };
    expect(localBusinessLd(pending)).toBeNull();
  });

  it("emits once the address is verified", () => {
    const withAddress: Vertical = {
      ...studio,
      nap: {
        ...studio.nap!,
        address: verified(
          {
            streetAddress: "Jalan Yos Sudarso",
            addressLocality: "Jajag",
            addressRegion: "Banyuwangi",
            addressCountry: "ID",
          },
          "owner",
        ),
      },
    };
    const ld = localBusinessLd(withAddress);
    expect(ld).toMatchObject({ "@type": "PhotographyBusiness" });
    expect(ld?.["parentOrganization"]).toEqual({ "@id": "https://bykami.id/#organization" });
  });

  it("carries makesOffer only for verified prices", () => {
    const base: Vertical = {
      ...studio,
      nap: {
        ...studio.nap!,
        address: verified(
          {
            streetAddress: "Jalan Yos Sudarso",
            addressLocality: "Jajag",
            addressRegion: "Banyuwangi",
            addressCountry: "ID",
          },
          "owner",
        ),
      },
    };

    expect(localBusinessLd(base)?.["makesOffer"]).toBeUndefined();

    const withPrice: Vertical = {
      ...base,
      offerings: [offering({ priceIDR: verified(45_000, "owner") })],
    };
    expect(localBusinessLd(withPrice)?.["makesOffer"]).toHaveLength(1);
  });

  it("never emits a LocalBusiness subtype for the platform root", () => {
    expect(localBusinessLd(byId("root"))).toBeNull();
  });
});

describe("social profiles reach sameAs only when verified", () => {
  const withSocial = (social: Vertical["social"]): Vertical => ({ ...studio, social });

  it("emits sameAs for an owner-verified handle", () => {
    const ld = organizationLd(
      withSocial({ instagram: verified("studiobykami", "owner") }),
    );
    expect(ld["sameAs"]).toEqual(["https://www.instagram.com/studiobykami/"]);
  });

  it("keeps an unverified handle out of sameAs", () => {
    const ld = organizationLd(
      withSocial({ instagram: unverified("boothbykami", "design notes") }),
    );
    expect(ld["sameAs"]).toBeUndefined();
  });

  it("builds a TikTok profile URL from the bare handle", () => {
    const ld = organizationLd(withSocial({ tiktok: verified("studiobykami", "owner") }));
    expect(ld["sameAs"]).toEqual(["https://www.tiktok.com/@studiobykami"]);
  });

  it("omits sameAs entirely for a vertical with no accounts", () => {
    expect(organizationLd(byId("root"))["sameAs"]).toBeUndefined();
  });

  it("rejects a URL where a handle belongs", () => {
    expect(() =>
      vertical.parse(
        withSocial({ instagram: verified("https://instagram.com/studiobykami", "owner") }),
      ),
    ).toThrow();
  });

  it("rejects a handle carrying its own @", () => {
    expect(() => vertical.parse(withSocial({ instagram: verified("@studiobykami", "owner") }))).toThrow();
  });
});

describe("embeddable posts", () => {
  it("builds a photo permalink under /p/ and a reel under /reel/", () => {
    expect(postUrl({ kind: "p", shortcode: "C1a2b3c4d5e" })).toBe(
      "https://www.instagram.com/p/C1a2b3c4d5e/",
    );
    expect(postUrl({ kind: "reel", shortcode: "C1a2b3c4d5e" })).toBe(
      "https://www.instagram.com/reel/C1a2b3c4d5e/",
    );
  });

  it("rejects a pasted permalink where a shortcode belongs", () => {
    expect(() =>
      vertical.parse({
        ...studio,
        social: {
          posts: verified(
            [{ kind: "p", shortcode: "https://www.instagram.com/p/C1a2b3c4d5e/" }],
            "owner",
          ),
        },
      }),
    ).toThrow();
  });

  it("does not let an empty list stand in for no list", () => {
    expect(() =>
      vertical.parse({ ...studio, social: { posts: verified([], "owner") } }),
    ).toThrow();
  });
});

describe("embeddable TikTok videos", () => {
  it("builds a video URL from the handle and the numeric id", () => {
    expect(videoUrl("studiobykami", { id: "7539932808274382087" })).toBe(
      "https://www.tiktok.com/@studiobykami/video/7539932808274382087",
    );
  });

  it("rejects a pasted URL where a video id belongs", () => {
    expect(() =>
      vertical.parse({
        ...studio,
        social: {
          videos: verified(
            [{ id: "https://www.tiktok.com/@studiobykami/video/7539932808274382087" }],
            "owner",
          ),
        },
      }),
    ).toThrow();
  });

  it("rejects a shortcode where a numeric id belongs", () => {
    expect(() =>
      vertical.parse({
        ...studio,
        social: { videos: verified([{ id: "DbGWkLRvPbQ" }], "owner") },
      }),
    ).toThrow();
  });
});

describe("entity consolidation", () => {
  it("points every vertical at the platform root", () => {
    for (const v of verticals.filter((v) => v.id !== "root")) {
      expect(organizationLd(v)["parentOrganization"]).toEqual({
        "@id": "https://bykami.id/#organization",
      });
    }
  });

  it("gives the root no parent", () => {
    expect(organizationLd(byId("root"))["parentOrganization"]).toBeUndefined();
  });
});

describe("current state of the catalogue", () => {
  it("has no owner-verified fact anywhere yet", () => {
    expect(launchReady(verticals)).toBe(false);
  });

  it("emits zero Offers across all four verticals", () => {
    const offers = verticals.flatMap((v) =>
      v.offerings.map((o) => offerLd(v, o)).filter((o) => o !== null),
    );
    expect(offers).toHaveLength(0);
  });

  it("reports both blocked and unverified gaps", () => {
    const all = gaps(verticals);
    expect(all.some((g) => g.status === "blocked")).toBe(true);
    expect(all.some((g) => g.status === "unverified")).toBe(true);
  });

  it("names the Dimsamcong menu as entirely absent rather than inventing one", () => {
    expect(byId("dimsamcong").offerings).toHaveLength(0);
  });
});
