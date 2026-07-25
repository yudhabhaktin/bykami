import type { Vertical } from "../schema.ts";
import { blocked } from "../sourced.ts";

/**
 * bykami.com — the platform root.
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
  displayName: "bykami",
  hostname: "bykami.com",
  tagline: "Studio foto, photobooth, dan kuliner di Banyuwangi",
  description:
    "bykami menaungi studio by KAMI, booth by KAMI, dan Dimsamcong di Banyuwangi, Jawa Timur.",
  schemaType: "Organization",

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
      question: "Apa itu bykami?",
      topic: "booking",
      answer: blocked(
        "Platform positioning copy needs owner sign-off before it is published as fact.",
      ),
    },
    {
      id: "berapa-usaha",
      question: "Usaha apa saja yang ada di bawah bykami?",
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
      question: "Bagaimana cara menghubungi bykami?",
      topic: "booking",
      answer: blocked("No platform-level contact route decided."),
    },
    {
      id: "lokasi-bykami",
      question: "Di mana bykami beroperasi?",
      topic: "location",
      answer: blocked("Owner should confirm how broadly to claim coverage."),
    },
  ],
};
