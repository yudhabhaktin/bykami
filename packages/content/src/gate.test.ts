import { describe, expect, it } from "vitest";
import { gaps, launchReady } from "./coverage.ts";
import { faqPageLd, localBusinessLd, offerLd, organizationLd } from "./jsonld.ts";
import type { Faq, Offering, Vertical } from "./schema.ts";
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
    expect(localBusinessLd(studio)).toBeNull();
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
    expect(ld?.["parentOrganization"]).toEqual({ "@id": "https://bykami.com/#organization" });
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

describe("entity consolidation", () => {
  it("points every vertical at the platform root", () => {
    for (const v of verticals.filter((v) => v.id !== "root")) {
      expect(organizationLd(v)["parentOrganization"]).toEqual({
        "@id": "https://bykami.com/#organization",
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
