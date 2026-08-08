import type { Vertical } from "../schema.ts";
import { blocked, unverified, verified } from "../sourced.ts";

const PDF = "refs/Price LIst Studio Indoor.pdf (owner PDF, not owner-confirmed)";

// The one fact here the owner has actually settled, and it contradicts the PDF.
// The booth enforces five minutes — it counts down on the capture screen and
// moves the customer on — so the PDF's fifteen is not a competing claim to be
// reconciled later, it is simply out of date. A drift guard in
// agent/internal/catalog asserts these two stay equal.
const OWNER_DURATION = "owner, 2026-08-01 — supersedes the PDF's 15 minutes";

/**
 * studio by KAMI — self-photo studio, pas foto, in Jajag, Banyuwangi.
 *
 * Every price below is read off the owner's price-list PDF, which is gitignored
 * and is not the same thing as the owner confirming it is current. They render
 * on the page and emit no `Offer` until someone confirms them.
 */
export const studio: Vertical = {
  id: "studio",
  displayName: "studio by KAMI",
  hostname: "studio.bykami.id",
  tagline: "Self photo studio di Banyuwangi",
  description:
    "Self photo studio, photobox, dan pas foto di Jajag, Banyuwangi. Sewa studio dengan lighting siap pakai, potret sendiri tanpa fotografer, hasil langsung cetak.",
  schemaType: "PhotographyBusiness",
  indexable: true,

  nap: {
    legalName: "studio by KAMI",
    displayName: "studio by KAMI",
    // The first owner-confirmed fact in the catalogue, and the one that opens
    // the LocalBusiness block.
    //
    // Two sources disagreed on the village: the price list says Jajag, and the
    // studio's own TikTok captions say Wringinagung. Both are in Gambiran
    // district, so neither is obviously a typo for the other and picking by
    // recency would have picked the wrong one — the captions are newer and they
    // are the ones that are wrong. The owner settled it: Jajag.
    //
    // Still no postal code. It is printed nowhere and a guessed one is a fake,
    // which is why the field is optional rather than filled in.
    address: verified(
      {
        streetAddress: "Jalan Yos Sudarso, Jajag Barat (Hotel Surya)",
        addressLocality: "Jajag, Gambiran",
        addressRegion: "Banyuwangi, Jawa Timur",
        addressCountry: "ID" as const,
      },
      "owner, 2026-08-09 — confirmed Jajag against the Wringinagung in the TikTok captions",
    ),
    mapsUrl: blocked("No Google Maps link in any source. Owner must supply."),
    // Every day, 09.00–21.00. The studio's own TikTok captions say so and the
    // owner confirmed it — which is the order that mattered, because those same
    // captions were the source that got the village wrong. A source being right
    // about one fact and wrong about another is the ordinary case, not a
    // surprise, and it is why confirmation is per-fact rather than per-source.
    //
    // No holiday or Lebaran variation recorded. "Mo-Su" claims there is none, so
    // if the studio closes for Idulfitri this is the line that will be wrong.
    openingHours: verified(
      ["Mo-Su 09:00-21:00"],
      "owner, 2026-08-09 — confirming the hours in the TikTok captions; no holiday variation given",
    ),
    whatsapp: unverified(
      "62811377710",
      `${PDF} — printed as "0811-3777-10", which is short for an ID mobile number and may be clipped`,
    ),
    bookingUrl: blocked(
      "YouCanBook.me calendars exist per design/booking-phase2.md but the URLs were never recorded.",
    ),
  },

  social: {
    instagram: verified(
      "studiobykami",
      "owner, 2026-08-08 — https://www.instagram.com/studiobykami/",
    ),
    tiktok: verified(
      "studiobykami",
      "owner, 2026-08-08 — https://www.tiktok.com/@studiobykami/video/7660164668975107346",
    ),
    /*
     * The owner's own selection, pasted as permalinks and stored newest-first in
     * the order they were sent — which is also roughly reverse-chronological,
     * since a shortcode encodes when it was minted.
     *
     * No captions. Instagram serves the signup wall to anything not logged in,
     * so nobody here has seen these posts; a caption written from the URL would
     * be a description of a photograph nobody looked at, which is the one thing
     * this package exists to prevent. The fallback text carries the generic
     * line until someone who can see them supplies the real ones.
     */
    posts: verified(
      [
        { kind: "p", shortcode: "DbGWkLRvPbQ" },
        { kind: "p", shortcode: "Da7aotSP667" },
        { kind: "p", shortcode: "DbmTvrMEqXd" },
        { kind: "p", shortcode: "DbiET7oE79V" },
        { kind: "p", shortcode: "DbLFSYAD0Gr" },
        // DbA-dqUEgTT was here and was withdrawn: it is a landscape post, and a
        // grid of portrait ones with a single wide row in the middle reads as a
        // mistake rather than as variety. The first thing anyone could tell us
        // about what these pictures actually look like, which is the check no
        // amount of URL validation was ever going to substitute for.
        { kind: "p", shortcode: "DaoaYU4k9v1" },
        { kind: "p", shortcode: "DZe8P0xD3w4" },
        { kind: "p", shortcode: "DZFN3IjH5gL" },
        { kind: "p", shortcode: "DZSKRCdH_Ou" },
        { kind: "p", shortcode: "DZC0pZbkubD" },
        { kind: "p", shortcode: "DYj7W_BE2Hx" },
        { kind: "p", shortcode: "DXOtRgOD1mI" },
      ],
      "owner, 2026-08-08 — permalinks supplied directly; each one checked to resolve",
    ),
    /*
     * Captions here are real, unlike the Instagram set above, and the difference
     * is worth naming: TikTok publishes an unauthenticated oembed endpoint, so
     * each video's own caption could be read back from tiktok.com rather than
     * guessed. Trimmed to the sentence a human wrote — the hashtag block and the
     * repeated address are noise in a fallback line.
     */
    videos: verified(
      [
        {
          id: "7539932808274382087",
          caption:
            "Terimakasih atas partisipasi Kamu yang sangat luar biasa. Kami tunggu kehadiran Kamu di lain waktu.",
        },
        { id: "7535857483055729928", caption: "Booking ur photo session now" },
        { id: "7535315445877001480", caption: "Booking ur photo session now" },
      ],
      "owner, 2026-08-08 — video URLs supplied directly; captions read from tiktok.com/oembed",
    ),
  },

  brand: {
    logoSvg: blocked("Original vector never supplied. See design/assets-needed.md."),
    accentColor: blocked("Derived from the logo vector, which is blocked."),
  },

  offerings: [
    {
      id: "mini",
      name: "MINI",
      serviceLine: "self-photo",
      orderIndex: 0,
      priceIDR: unverified(45_000, PDF),
      durationMinutes: verified(5, OWNER_DURATION),
      printsIncluded: unverified(1, PDF),
      headcount: unverified({ min: 1, max: 2 }, `${PDF} — conflicts with YouCanBook.me`),
    },
    {
      id: "midi",
      name: "MIDI",
      serviceLine: "self-photo",
      orderIndex: 1,
      priceIDR: unverified(70_000, PDF),
      durationMinutes: unverified(20, PDF),
      printsIncluded: unverified(2, PDF),
      headcount: unverified({ min: 1, max: 4 }, `${PDF} — conflicts with YouCanBook.me`),
    },
    {
      id: "maxi",
      name: "MAXI",
      serviceLine: "self-photo",
      orderIndex: 2,
      priceIDR: unverified(95_000, PDF),
      durationMinutes: unverified(20, PDF),
      printsIncluded: unverified(2, PDF),
      headcount: unverified({ min: 1, max: 6 }, `${PDF} — conflicts with YouCanBook.me`),
    },
    {
      id: "big-maxi",
      name: "BIG MAXI",
      serviceLine: "self-photo",
      orderIndex: 3,
      priceIDR: unverified(165_000, PDF),
      durationMinutes: unverified(25, PDF),
      printsIncluded: unverified(3, PDF),
      headcount: unverified({ min: 1, max: 10 }, `${PDF} — conflicts with YouCanBook.me`),
    },
    {
      id: "pas-foto",
      name: "Pas Foto (close up)",
      serviceLine: "pas-foto",
      description: "Maksimal 15x take, 1 print 4R (bisa custom ukuran), 1 file edit.",
      orderIndex: 4,
      priceIDR: unverified(50_000, `${PDF} — per orang`),
      printsIncluded: unverified(1, PDF),
      headcount: unverified({ min: 1, max: 1 }, PDF),
    },
    {
      id: "marry-me",
      name: "Marry Me (pas foto)",
      serviceLine: "pas-foto",
      description: "1 background, 2 print 4R, 6 file edit.",
      orderIndex: 5,
      priceIDR: unverified(90_000, `${PDF} — per 2 orang`),
      durationMinutes: unverified(20, PDF),
      printsIncluded: unverified(2, PDF),
      headcount: unverified({ min: 2, max: 2 }, PDF),
    },
    {
      id: "nikah-dinas",
      name: "Pas Foto Nikah Dinas",
      serviceLine: "pas-foto",
      description: "2 background, 5 print 4R (bisa custom ukuran), maksimal 10 file.",
      orderIndex: 6,
      priceIDR: unverified(250_000, `${PDF} — per 2 orang`),
      durationMinutes: unverified(40, PDF),
      printsIncluded: unverified(5, PDF),
      headcount: unverified({ min: 2, max: 2 }, PDF),
    },
  ],

  backdrops: [],

  promos: [
    {
      id: "bogo",
      headline: "Buy 1 Get 1",
      mechanic: blocked(
        "Seen in Instagram captions (design/direction.md). Terms, eligible packages, and end date all unknown.",
      ),
    },
  ],

  faqs: [
    {
      id: "apa-itu-self-photo",
      question: "Apa itu self photo studio?",
      topic: "session",
      answer: unverified(
        "Kamu sewa ruang studio yang sudah tertata lighting-nya, lalu memotret sendiri pakai remote shutter. Tidak ada fotografer di dalam ruangan, jadi kamu bebas bergaya sesuai waktu yang kamu pesan.",
        "Format description in design/direction.md",
      ),
    },
    {
      id: "paket-mana",
      question: "Paket mana yang cocok untuk rombongan saya?",
      topic: "capacity",
      answer: blocked(
        "Depends on the unresolved capacity conflict — the PDF and the booking calendar disagree on headcount per package.",
      ),
    },
    {
      id: "durasi-mini",
      question: "Berapa lama durasi paket MINI?",
      topic: "session",
      // Still unverified as a whole: the duration is settled, the headcount and
      // the print are the PDF's word. Kept together because the sentence a
      // customer reads is one claim, and publishing half of it is not an option
      // the FAQ shape offers.
      answer: unverified(
        "5 menit untuk 1–2 orang, termasuk 1 cetak 4R.",
        `${PDF}; durasi dari ${OWNER_DURATION}`,
      ),
    },
    {
      id: "durasi-big-maxi",
      question: "Berapa lama durasi paket BIG MAXI?",
      topic: "session",
      answer: unverified("25 menit untuk sampai 10 orang, termasuk 3 cetak 4R.", PDF),
    },
    {
      id: "dapat-file",
      question: "Apakah saya dapat file digitalnya?",
      topic: "results",
      answer: unverified(
        "Paket self photo tertulis “all file” dengan syarat dan ketentuan. Detail ketentuannya belum kami konfirmasi.",
        `${PDF} — "All file t&c", terms never spelled out`,
      ),
    },
    {
      id: "kapan-file-dikirim",
      question: "Berapa lama file saya dikirim?",
      topic: "results",
      answer: blocked("Delivery window is unknown. Owner must confirm."),
    },
    {
      id: "harus-booking",
      question: "Harus booking dulu atau bisa langsung datang?",
      topic: "booking",
      answer: blocked(
        "Walk-in policy unknown. The PDF says chat WA admin to book, but does not say whether walk-ins are accepted.",
      ),
    },
    {
      id: "cara-booking",
      question: "Bagaimana cara booking?",
      topic: "booking",
      answer: unverified(
        "Chat admin lewat WhatsApp untuk memesan slot.",
        `${PDF} — "*Chat WA Admin untuk booking"`,
      ),
    },
    {
      id: "bayar-dp",
      question: "Apakah harus bayar DP?",
      topic: "booking",
      answer: unverified(
        "Booking tanpa DP — pembayaran dilakukan di studio.",
        "Tagline BOOKING TANPA DP recorded in design/booking-phase2.md",
      ),
    },
    {
      id: "reschedule",
      question: "Bisa reschedule atau batal?",
      topic: "policy",
      answer: blocked("No reschedule or cancellation policy exists in any source."),
    },
    {
      id: "bawa-properti",
      question: "Boleh bawa properti sendiri?",
      topic: "policy",
      answer: blocked("Props policy unknown."),
    },
    {
      id: "bawa-hewan",
      question: "Boleh bawa hewan peliharaan?",
      topic: "policy",
      answer: blocked("Pet policy unknown."),
    },
    {
      id: "ganti-background",
      question: "Bisa ganti background saat sesi berlangsung?",
      topic: "session",
      answer: blocked("Backdrop-switching rules unknown; the backdrop list itself is missing."),
    },
    {
      id: "pas-foto-ukuran",
      question: "Ukuran pas foto bisa custom?",
      topic: "results",
      answer: unverified(
        "Bisa. Cetak 4R dan ukuran bisa disesuaikan.",
        `${PDF} — "1 PRINT 4R (bs custom ukuran)"`,
      ),
    },
    {
      id: "lokasi",
      question: "Di mana lokasi studionya?",
      topic: "location",
      answer: unverified(
        "Jalan Yos Sudarso, Jajag Barat (Hotel Surya), Banyuwangi.",
        `${PDF} — footer line`,
      ),
    },
    {
      id: "jam-buka",
      question: "Jam berapa studio buka?",
      topic: "location",
      answer: blocked("Opening hours unknown."),
    },
    {
      id: "parkir",
      question: "Ada tempat parkir?",
      topic: "location",
      answer: blocked("Parking unknown."),
    },
  ],
};
