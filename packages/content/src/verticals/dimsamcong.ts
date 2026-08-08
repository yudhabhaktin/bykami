import type { Vertical } from "../schema.ts";
import { blocked } from "../sourced.ts";

/**
 * Dimsamcong — the F&B vertical.
 *
 * There is no source material for this business anywhere in the project. The
 * other three verticals have owner PDFs; this one has an Instagram mention and
 * nothing else. So every fact is `blocked`, the menu is empty, and the site will
 * render a real but small page rather than an empty menu table.
 *
 * Spelling is "Dimsamcong" throughout, matching README.md and
 * design/platform-architecture.md.
 */
export const dimsamcong: Vertical = {
  id: "dimsamcong",
  displayName: "Dimsamcong",
  hostname: "dimsamcong.bykami.id",
  tagline: "Dimsum di Banyuwangi",
  description:
    "Dimsamcong — dimsum dan makanan ringan di Banyuwangi, bagian dari bykami.",
  schemaType: "Restaurant",
  /**
   * Held back deliberately. Every fact on this property is still `blocked` and
   * `offerings` is empty, so the page has nothing a searcher could want. Letting
   * it in would trade a permanent thin-content judgement for zero traffic.
   * Flip this in the same commit that lands the menu.
   */
  indexable: false,

  nap: {
    legalName: "Dimsamcong",
    displayName: "Dimsamcong",
    address: blocked("No outlet address recorded anywhere in the project."),
    mapsUrl: blocked("No Google Maps link."),
    openingHours: blocked("Opening hours unknown."),
    whatsapp: blocked("No contact number recorded for the F&B vertical."),
    bookingUrl: blocked("Not applicable until ordering exists."),
  },

  social: {
    instagram: blocked(
      "The vertical is known to have an Instagram account, but no handle was ever recorded.",
    ),
    tiktok: blocked("Owner has not said whether a TikTok account exists."),
  },

  brand: {
    logoSvg: blocked("No brand assets for the F&B vertical."),
    accentColor: blocked("No brand assets for the F&B vertical."),
  },

  /** Empty on purpose. No menu source exists; an invented menu is worse than none. */
  offerings: [],
  backdrops: [],
  promos: [],

  faqs: [
    {
      id: "apa-itu-dimsamcong",
      question: "Apa itu Dimsamcong?",
      topic: "menu",
      answer: blocked("Positioning and menu unconfirmed."),
    },
    {
      id: "menu-dimsamcong",
      question: "Apa saja menunya?",
      topic: "menu",
      answer: blocked("No menu exists in any source."),
    },
    {
      id: "lokasi-dimsamcong",
      question: "Di mana lokasinya?",
      topic: "location",
      answer: blocked("Outlet address unknown."),
    },
    {
      id: "delivery-dimsamcong",
      question: "Bisa pesan antar?",
      topic: "menu",
      answer: blocked("Delivery availability unknown."),
    },
    {
      id: "halal-dimsamcong",
      question: "Apakah sudah bersertifikat halal?",
      topic: "menu",
      answer: blocked(
        "Halal certification status unknown — must never be guessed, it is a legal claim.",
      ),
    },
  ],
};
