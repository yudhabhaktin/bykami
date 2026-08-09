import type { Vertical } from "../schema.ts";
import { blocked } from "../sourced.ts";
import { houseWhatsapp } from "./contact.ts";

/**
 * bykami.id — the platform root.
 *
 * Brand, a directory of the verticals, and the sitemap index. It has no physical
 * location, so it is an `Organization` and never a `LocalBusiness` subtype; every
 * vertical points its `parentOrganization` here, which is what consolidates four
 * hostnames into one entity.
 *
 * The account and loyalty portal described in design/platform-architecture.md is
 * phase 2 and deliberately absent.
 */
export const root: Vertical = {
  id: "root",
  displayName: "by KAMI",
  hostname: "bykami.id",
  tagline: "Studio foto, photobooth, dan kuliner di Banyuwangi",
  description:
    "by KAMI menaungi studio by KAMI dan booth by KAMI, serta menjalankan outlet Dimsamcong di Banyuwangi, Jawa Timur.",
  schemaType: "Organization",
  indexable: true,

  /*
   * A contact route, and deliberately nothing else. The root has no premises —
   * that is why it is an `Organization` and never a `LocalBusiness` — so address,
   * map, and hours stay blocked and the footer renders none of them. What it does
   * have now is a line someone can reach the company on, which is what the CTA
   * needs and what the "no platform-level contact route decided" FAQ was waiting
   * on.
   */
  nap: {
    legalName: "by KAMI",
    displayName: "by KAMI",
    address: blocked("The platform root has no premises of its own."),
    mapsUrl: blocked("No location to map — see address."),
    openingHours: blocked("The root is not a place that opens and closes."),
    whatsapp: houseWhatsapp,
    bookingUrl: blocked("Booking happens on a vertical, never at the root."),
  },

  brand: {
    logoSvg: blocked("Original vector never supplied. See design/assets-needed.md."),
    accentColor: blocked("Derived from the logo vector, which is blocked."),
  },

  offerings: [],
  backdrops: [],
  promos: [],

  faqs: [
    {
      id: "apa-itu-bykami",
      question: "Apa itu by KAMI?",
      topic: "booking",
      answer: blocked(
        "Platform positioning copy needs owner sign-off before it is published as fact.",
      ),
    },
    {
      id: "berapa-usaha",
      question: "Usaha apa saja yang ada di bawah by KAMI?",
      topic: "booking",
      answer: blocked(
        "Three are operating, but whether all should be named publicly is the owner's call.",
      ),
    },
    {
      id: "satu-perusahaan",
      question: "Apakah studio by KAMI dan booth by KAMI satu perusahaan?",
      topic: "booking",
      answer: blocked(
        "Legal entity structure is unresolved — see design/platform-architecture.md open questions.",
      ),
    },
    {
      id: "kontak-bykami",
      question: "Bagaimana cara menghubungi by KAMI?",
      topic: "booking",
      answer: blocked("No platform-level contact route decided."),
    },
    {
      id: "lokasi-bykami",
      question: "Di mana by KAMI beroperasi?",
      topic: "location",
      answer: blocked("Owner should confirm how broadly to claim coverage."),
    },
  ],
};
