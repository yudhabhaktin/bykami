import { z } from "zod";
import { sourced } from "./sourced.ts";

/**
 * One priced thing a vertical sells. Photo packages and menu items differ only in
 * which optional fields apply, so they share a record rather than forcing a
 * discriminated union that would buy nothing.
 */
export const offering = z.object({
  id: z.string().min(1),
  name: z.string().min(1),
  serviceLine: z.enum([
    "self-photo",
    "photobox",
    "pas-foto",
    "in-studio-photographer",
    "food",
    "drink",
  ]),
  description: z.string().optional(),
  orderIndex: z.number().int().nonnegative(),

  priceIDR: sourced(z.number().int().positive()),
  durationMinutes: sourced(z.number().int().positive()).optional(),
  printsIncluded: sourced(z.number().int().nonnegative()).optional(),
  headcount: sourced(
    z
      .object({
        min: z.number().int().positive(),
        max: z.number().int().positive(),
      })
      .refine((h) => h.max >= h.min, {
        message: "headcount.max must be >= headcount.min",
      }),
  ).optional(),
});

export const backdrop = z.object({
  id: z.string().min(1),
  name: z.string().min(1),
  theme: z.string().min(1),
  image: sourced(z.string().min(1)),
  available: z.boolean(),
});

export const promo = z.object({
  id: z.string().min(1),
  headline: z.string().min(1),
  mechanic: sourced(z.string().min(1)),
  validUntil: sourced(z.string().min(1)).optional(),
});

/**
 * Answers are `Sourced` because they reach `FAQPage` structured data, which LLMs
 * quote verbatim as fact. A plausible-sounding guess about the reschedule policy
 * is a liability, not a placeholder.
 */
export const faq = z.object({
  id: z.string().min(1),
  question: z.string().min(1),
  answer: sourced(z.string().min(1)),
  topic: z.enum([
    "booking",
    "pricing",
    "capacity",
    "session",
    "results",
    "policy",
    "location",
    "menu",
  ]),
});

export const nap = z.object({
  legalName: z.string().min(1),
  displayName: z.string().min(1),
  address: sourced(
    z.object({
      streetAddress: z.string().min(1),
      addressLocality: z.string().min(1),
      addressRegion: z.string().min(1),
      /** Optional: absent from every source we hold, and a guessed one is a fake. */
      postalCode: z.string().min(1).optional(),
      addressCountry: z.literal("ID"),
    }),
  ),
  mapsUrl: sourced(z.url()),
  /** schema.org `openingHours` strings, e.g. "Mo-Su 09:00-21:00". */
  openingHours: sourced(z.array(z.string().min(1)).min(1)),
  whatsapp: sourced(z.string().regex(/^62\d{8,13}$/, "E.164 without +, ID only")),
  bookingUrl: sourced(z.url()).optional(),
});

export const brand = z.object({
  logoSvg: sourced(z.string().min(1)),
  accentColor: sourced(z.string().regex(/^#[0-9a-fA-F]{6}$/)),
});

export const vertical = z.object({
  id: z.enum(["root", "studio", "dimsamcong", "booth"]),
  displayName: z.string().min(1),
  hostname: z.string().min(1),
  tagline: z.string().min(1),
  description: z.string().min(1),
  /**
   * schema.org type. `PhotographyBusiness` and `Restaurant` are both
   * `LocalBusiness` subtypes and are more specific than competitors publish.
   * The platform root is an `Organization` only — it has no physical location.
   */
  schemaType: z.enum([
    "Organization",
    "PhotographyBusiness",
    "Restaurant",
  ]),
  /**
   * Whether this property has enough content to deserve a place in the index.
   *
   * Separate from the BYKAMI_INDEXABLE environment gate, which answers a
   * different question: *is this the real domain, or a pages.dev preview?* Both
   * must be true before anything is indexed. Environment is CI's call; this is
   * an editorial one and belongs in the repo next to the catalogue it describes.
   *
   * A property with no catalogue is worse than absent: it teaches search engines
   * that the hostname is thin, and that judgement outlives the empty page.
   */
  indexable: z.boolean(),
  nap: nap.optional(),
  brand,
  offerings: z.array(offering),
  backdrops: z.array(backdrop),
  promos: z.array(promo),
  faqs: z.array(faq),
});

export type Offering = z.infer<typeof offering>;
export type Backdrop = z.infer<typeof backdrop>;
export type Promo = z.infer<typeof promo>;
export type Faq = z.infer<typeof faq>;
export type Nap = z.infer<typeof nap>;
export type Brand = z.infer<typeof brand>;
export type Vertical = z.infer<typeof vertical>;

export const PLATFORM_ROOT = "https://bykami.id";
