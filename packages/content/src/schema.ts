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
  /**
   * Which line of business the offering belongs to.
   *
   * `outdoor-photographer` and `videographer` are the packages sold *away* from
   * the studio — a graduation, an aqiqah, a run — and they are separate from
   * `in-studio-photographer` because almost nothing about them is the same
   * transaction: they are priced in hours rather than minutes, they happen at the
   * customer's location, and they are the only thing here whose deliverable is a
   * Drive folder rather than a print. Their own price list is titled "DILUAR
   * STUDIO", which is the distinction the studio itself draws.
   *
   * These strings are also the vocabulary `booking_services.service_line` is
   * checked against in the API, so the two halves of a booking describe a package
   * the same way.
   */
  serviceLine: z.enum([
    "self-photo",
    "photobox",
    "pas-foto",
    "in-studio-photographer",
    "outdoor-photographer",
    "videographer",
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
  /**
   * Where a customer picks a time.
   *
   * An absolute URL or a site-relative path, and it accepts the second because
   * booking came in-house. This was an external YouCanBook.me address when the
   * field was written, so a full URL was the only shape it could take; the
   * calendar it named is gone and the replacement is a page on the vertical's
   * own site. Writing that as `https://studio.bykami.id/booking` would be a
   * cross-origin spelling of a same-origin link — it would leave `astro dev`
   * for production mid-flow, and it is the one link on the site a customer
   * follows while deciding.
   *
   * Same rule as Nav, which links its own property with "/" and the other three
   * by hostname.
   */
  bookingUrl: sourced(
    z.union([z.url(), z.string().regex(/^\/\S*$/, "a site-relative path, e.g. /booking")]),
  ).optional(),
});

export const brand = z.object({
  logoSvg: sourced(z.string().min(1)),
  accentColor: sourced(z.string().regex(/^#[0-9a-fA-F]{6}$/)),
});

/**
 * A profile is stored as the handle alone — never the full URL.
 *
 * The URL is one template away from the handle, and holding both is holding two
 * things that can disagree. The handle is also what a customer recognises and
 * what gets said out loud, so it is the fact; the link is a rendering of it.
 */
const handle = z
  .string()
  .regex(/^[A-Za-z0-9._]{1,30}$/, "handle only — no leading @, no URL");

/**
 * One embeddable post.
 *
 * `kind` is stored rather than derived because it is the one part of the
 * permalink the shortcode does not tell you — Instagram serves a reel under
 * `/reel/` and a photo under `/p/`, and guessing wrong is an embed that renders
 * as a dead link. Only public posts can be embedded at all.
 */
export const post = z.object({
  kind: z.enum(["p", "reel"]),
  shortcode: z.string().regex(/^[A-Za-z0-9_-]{5,24}$/, "shortcode only — no URL"),
  /**
   * Fallback text, shown before the embed script runs and to anyone it never
   * runs for. Instagram's own default is "View this post on Instagram", which
   * tells a crawler nothing — a real description is the only indexable text an
   * embed contributes.
   */
  caption: z.string().min(1).optional(),
});

/**
 * One embeddable TikTok video.
 *
 * A separate record from `post` rather than another `kind` on it, because the
 * two share no structure: TikTok identifies a video by a numeric id and puts the
 * account handle in the path, so building the URL needs the handle that Instagram
 * does not. Collapsing them would mean a record where half the fields are always
 * empty and the reader has to know which half.
 */
export const video = z.object({
  id: z.string().regex(/^\d{6,32}$/, "numeric video id only — no URL"),
  caption: z.string().min(1).optional(),
});

export const social = z.object({
  instagram: sourced(handle).optional(),
  tiktok: sourced(handle).optional(),
  /** Hand-picked posts to embed. Curation is the owner's call, so it carries provenance like any other supplied fact. */
  posts: sourced(z.array(post).min(1)).optional(),
  /** The same, for TikTok. Requires `tiktok` to be set — the handle is part of every video's URL. */
  videos: sourced(z.array(video).min(1)).optional(),
});

export const instagramUrl = (handle: string) => `https://www.instagram.com/${handle}/`;
export const tiktokUrl = (handle: string) => `https://www.tiktok.com/@${handle}`;
export const postUrl = (p: Post) => `https://www.instagram.com/${p.kind}/${p.shortcode}/`;
export const videoUrl = (handle: string, v: Video) =>
  `https://www.tiktok.com/@${handle}/video/${v.id}`;

export const vertical = z.object({
  id: z.enum(["root", "studio", "dimsamcong", "booth"]),
  displayName: z.string().min(1),
  /**
   * The name to use where the four properties are listed together, and the only
   * place a shortened brand is correct.
   *
   * Three of the four display names end in "by KAMI", which is right on a page
   * title and wrong in a row of four links: the cross-property nav rendered
   * "studio by KAMI · by KAMI · booth by KAMI · Dimsamcong Banyuwangi", where
   * the shared half is 132px of a 350px phone header spent saying the same
   * three syllables four times. It is what forced that header onto two rows.
   *
   * Only the nav uses it. Titles, JSON-LD, llms.txt and the footer stay on
   * `displayName`, because in each of those the full name is the point.
   */
  shortName: z.string().min(1),
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
  /** Absent where the vertical has no account of its own, which is not the same as an unknown handle. */
  social: social.optional(),
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
export type Post = z.infer<typeof post>;
export type Video = z.infer<typeof video>;
export type Social = z.infer<typeof social>;
export type Brand = z.infer<typeof brand>;
export type Vertical = z.infer<typeof vertical>;

export const PLATFORM_ROOT = "https://bykami.id";
