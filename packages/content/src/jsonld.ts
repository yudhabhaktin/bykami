import { PLATFORM_ROOT, type Faq, type Offering, type Vertical } from "./schema.ts";
import { publishedValueOf } from "./sourced.ts";

type Ld = Record<string, unknown>;

const siteUrl = (v: Vertical) => `https://${v.hostname}`;

/**
 * Every property declares itself part of one company, so engines consolidate
 * three hostnames into a single entity. Link authority does not flow between
 * subdomains, but entity understanding does.
 */
export const organizationLd = (v: Vertical): Ld => {
  const ld: Ld = {
    "@context": "https://schema.org",
    "@type": "Organization",
    "@id": `${siteUrl(v)}/#organization`,
    name: v.displayName,
    url: siteUrl(v),
    description: v.description,
  };
  if (v.id !== "root") {
    ld["parentOrganization"] = { "@id": `${PLATFORM_ROOT}/#organization` };
  }
  const logo = publishedValueOf(v.brand.logoSvg);
  if (logo) ld["logo"] = `${siteUrl(v)}${logo}`;
  return ld;
};

/**
 * `Offer` is emitted only from an owner-verified price.
 *
 * This is the single most important rule in the package. An unverified price
 * that reaches structured data gets quoted back by search and LLMs as fact, and
 * contradicts the live booking calendar exactly when a customer is deciding.
 * Publishing nothing is recoverable; publishing a wrong number is not.
 */
export const offerLd = (v: Vertical, o: Offering): Ld | null => {
  const price = publishedValueOf(o.priceIDR);
  if (price === undefined) return null;

  const ld: Ld = {
    "@type": "Offer",
    "@id": `${siteUrl(v)}/#offer-${o.id}`,
    name: o.name,
    price: String(price),
    priceCurrency: "IDR",
    availability: "https://schema.org/InStock",
    url: siteUrl(v),
  };
  if (o.description) ld["description"] = o.description;
  return ld;
};

/**
 * A `LocalBusiness` subtype without a verified address is a worse entity than no
 * subtype at all, so the address gates the whole block. Verticals with no
 * physical location (the platform root) never emit one.
 */
export const localBusinessLd = (v: Vertical): Ld | null => {
  if (v.schemaType === "Organization" || !v.nap) return null;

  const address = publishedValueOf(v.nap.address);
  if (!address) return null;

  const ld: Ld = {
    "@context": "https://schema.org",
    "@type": v.schemaType,
    "@id": `${siteUrl(v)}/#business`,
    name: v.nap.displayName,
    legalName: v.nap.legalName,
    url: siteUrl(v),
    description: v.description,
    parentOrganization: { "@id": `${PLATFORM_ROOT}/#organization` },
    address: { "@type": "PostalAddress", ...address },
  };

  const hours = publishedValueOf(v.nap.openingHours);
  if (hours) ld["openingHours"] = hours;

  const maps = publishedValueOf(v.nap.mapsUrl);
  if (maps) ld["hasMap"] = maps;

  const whatsapp = publishedValueOf(v.nap.whatsapp);
  if (whatsapp) ld["telephone"] = `+${whatsapp}`;

  const offers = v.offerings
    .map((o) => offerLd(v, o))
    .filter((o): o is Ld => o !== null);
  if (offers.length > 0) ld["makesOffer"] = offers;

  return ld;
};

/** Only owner-verified answers. A guessed policy quoted as fact is a liability. */
export const faqPageLd = (v: Vertical, faqs: Faq[]): Ld | null => {
  const entries = faqs
    .map((f): Ld | null => {
      const answer = publishedValueOf(f.answer);
      if (!answer) return null;
      return {
        "@type": "Question",
        name: f.question,
        acceptedAnswer: { "@type": "Answer", text: answer },
      };
    })
    .filter((e): e is Ld => e !== null);

  if (entries.length === 0) return null;

  return {
    "@context": "https://schema.org",
    "@type": "FAQPage",
    "@id": `${siteUrl(v)}/#faq`,
    mainEntity: entries,
  };
};

export const breadcrumbLd = (
  v: Vertical,
  trail: { name: string; path: string }[],
): Ld => ({
  "@context": "https://schema.org",
  "@type": "BreadcrumbList",
  itemListElement: trail.map((t, i) => ({
    "@type": "ListItem",
    position: i + 1,
    name: t.name,
    item: `${siteUrl(v)}${t.path}`,
  })),
});

/** Every block a page should carry, with unpublishable ones already dropped. */
export const pageLd = (
  v: Vertical,
  trail: { name: string; path: string }[] = [],
): Ld[] => {
  const blocks: (Ld | null)[] = [
    organizationLd(v),
    localBusinessLd(v),
    faqPageLd(v, v.faqs),
    trail.length > 0 ? breadcrumbLd(v, trail) : null,
  ];
  return blocks.filter((b): b is Ld => b !== null);
};
