import type { Vertical } from "../schema.ts";
import { blocked, unverified, verified } from "../sourced.ts";

/**
 * Dimsamcong — the F&B vertical.
 *
 * Unlike the other three, this is not a by KAMI brand. Dimsamcong is an existing
 * Jember business; by KAMI runs its Banyuwangi branch (owner, 2026-08-09). The
 * brand's own outlets — Jl. Sumatra, PB Sudirman, Pasar Sabtuan, Mastrip, Roxy,
 * Bondowoso, Kalisat — belong to the franchisor, not to this property, so they
 * are recorded here as context and never published as this vertical's locations.
 *
 * That ownership split is why the copy says by KAMI *runs the outlet* rather
 * than that Dimsamcong is *part of* by KAMI, and why every field here is careful
 * to describe the branch and not the brand.
 *
 * The outlet shares premises with studio by KAMI in Jajag, which is what opened
 * the address, the location FAQ, and with them the `LocalBusiness` block.
 *
 * There is still no menu source anywhere in the project, so `offerings` is empty
 * and the page stays deliberately small rather than padded with invented dishes.
 *
 * Spelling is "Dimsamcong" throughout, matching README.md and
 * design/platform-architecture.md.
 */
export const dimsamcong: Vertical = {
  id: "dimsamcong",
  /**
   * The outlet, not the brand. `organizationLd` names this node and hands it
   * `parentOrganization: bykami.id`, so "Dimsamcong" alone published the claim
   * that the Jember brand is a by KAMI subsidiary. Naming the branch by KAMI
   * actually runs makes that claim true as written.
   */
  displayName: "Dimsamcong Banyuwangi",
  hostname: "dimsamcong.bykami.id",
  tagline: "Dimsum di Jajag, Banyuwangi",
  description:
    "Dimsamcong — dimsum dan makanan ringan di Jajag, Banyuwangi. Outlet yang dijalankan oleh by KAMI, satu lokasi dengan studio by KAMI.",
  schemaType: "Restaurant",
  /**
   * Held back deliberately, and now for one reason rather than several. The
   * address and the location FAQ are answered, so the page is no longer empty —
   * but `offerings` is still empty, and a Restaurant with no menu is the thin
   * page a searcher bounces off. Flip this in the same commit that lands the
   * menu, not before.
   */
  indexable: false,

  nap: {
    /**
     * The trade name, not a registered company — the entity that operates the
     * outlet has never been recorded. That follows `studio.ts`, where `legalName`
     * is likewise the name over the door, and it is the branch rather than the
     * brand for the same reason `displayName` is.
     */
    legalName: "Dimsamcong Banyuwangi",
    displayName: "Dimsamcong Banyuwangi",
    /**
     * The same premises as studio by KAMI — owner, 2026-08-09, and the one fact
     * two independent Instagram sources agree on: @culinary.mince announced the
     * opening "di depan studio by KAMI", and the outlet's own account gives its
     * location as "barat NEW Surya Hotel, Jajag".
     *
     * Copied from `studio.ts` rather than shared, because these are two
     * businesses that happen to sit at one address, not one address used twice —
     * either could move without the other. If one does move, this comment is how
     * the next person finds the other copy.
     *
     * Still no postal code, for the same reason as the studio: it is printed
     * nowhere and a guessed one is a fake.
     */
    address: verified(
      {
        streetAddress: "Jalan Yos Sudarso, Jajag Barat (Hotel Surya)",
        addressLocality: "Jajag, Gambiran",
        addressRegion: "Banyuwangi, Jawa Timur",
        addressCountry: "ID" as const,
      },
      "owner, 2026-08-09 — same premises as studio by KAMI",
    ),
    mapsUrl: blocked("No Google Maps link."),
    openingHours: blocked(
      "The brand account says \"BUKA SETIAP HARI BANGET\", which is a slogan and not hours. The outlet's own account lists none.",
    ),
    /**
     * @dimsamcong.jajag lists 0811-222-521, and the owner has now confirmed it is
     * the line by KAMI answers for this outlet (2026-08-09) — which is what
     * released it. The caution was well placed: `StickyBar` turns this field into
     * a WhatsApp button, so a wrong number here is a customer sent somewhere
     * else. (The brand account's 0811-311-1888-1 is the franchisor's and was
     * never a candidate.)
     *
     * The one vertical not on the shared line in verticals/contact.ts, and the
     * franchise split explains both: by KAMI runs this branch rather than owning
     * the brand, so a contact route of its own is the expected shape here.
     */
    whatsapp: verified(
      "62811222521",
      "owner, 2026-08-09 — confirmed the outlet answers 0811-222-521, as published in the @dimsamcong.jajag bio",
    ),
    bookingUrl: blocked("Not applicable until ordering exists."),
  },

  social: {
    /**
     * The outlet's own account, not the brand's. @dimsamcong.idn is the
     * franchisor — its bio lists the Jember outlets and a different WhatsApp —
     * and this field is an identity claim in two renderers (`Footer.astro` links
     * it `rel="me"`, `jsonld.ts` would publish it as `sameAs`), so pointing it at
     * the franchisor would hand this property's authority to another business.
     *
     * @dimsamcong.jajag is the right account — full name "DIMSAMCONG JAJAG
     * BANYUWANGI", bio "Part of @dimsamcong.idn", location "barat NEW Surya
     * Hotel". Unverified rather than verified because it was read off Instagram
     * on 2026-08-09, not confirmed by the owner, which keeps it out of `sameAs`
     * while still linking it for a human.
     */
    instagram: unverified(
      "dimsamcong.jajag",
      "read off https://www.instagram.com/dimsamcong.jajag/ on 2026-08-09 — profile names the Jajag outlet and credits @dimsamcong.idn as the brand",
    ),
    tiktok: blocked("Owner has not said whether a TikTok account exists."),
    /**
     * Owner-supplied permalinks, so the curation is confirmed even though two of
     * the three are other people's posts — @culinary.mince covering the opening
     * and @dimsamcong.jajag's own. Captions are trimmed from the real ones and
     * drop the hashtag tails; the buy-1-get-1 in the opening post is deliberately
     * left out, being a dated promo that has long since expired.
     */
    posts: verified(
      [
        {
          kind: "p" as const,
          shortcode: "DaotxbWzXnS",
          caption: "Dimsamcong buka di Jajag, tepat di depan studio by KAMI.",
        },
        { kind: "p" as const, shortcode: "DauHblpPrS5" },
        {
          kind: "p" as const,
          shortcode: "DboFBxZk6ls",
          caption: "Dimsamcong resmi hadir di Banyuwangi — dimsum dan mentai.",
        },
      ],
      "owner, 2026-08-09 — supplied these three permalinks",
    ),
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
      answer: verified(
        "Di Jalan Yos Sudarso, Jajag Barat, Gambiran, Banyuwangi — sebelah barat Hotel Surya, satu lokasi dengan studio by KAMI.",
        "owner, 2026-08-09 — same premises as studio by KAMI",
      ),
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
