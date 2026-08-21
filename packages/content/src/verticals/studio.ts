import type { Vertical } from "../schema.ts";
import { blocked, unverified, verified } from "../sourced.ts";
import { houseWhatsapp } from "./contact.ts";

const PDF = "refs/Price LIst Studio Indoor.pdf (owner PDF, not owner-confirmed)";

// The one fact here the owner has actually settled, and it contradicts the PDF.
// The booth enforces five minutes — it counts down on the capture screen and
// moves the customer on — so the PDF's fifteen is not a competing claim to be
// reconciled later, it is simply out of date. A drift guard in
// agent/internal/catalog asserts these two stay equal.
const OWNER_DURATION = "owner, 2026-08-01 — supersedes the PDF's 15 minutes";

/**
 * The studio's own booking pages, read through YouCanBook.me's JSON API on
 * 2026-08-10 — `studiobykami-self` and `studiobykami-photobox`.
 *
 * A stronger source than the PDF for anything it covers, and the reason is not
 * that it is newer: it is the thing customers were transacting against. A price
 * on this page was the price somebody paid that afternoon, where the PDF is a
 * document that may or may not have been kept up to date. It is still not the
 * owner's word, so everything from it stays `unverified`.
 *
 * It disagrees with the PDF in two places worth knowing about. It carried a whole
 * service line the PDF has never mentioned — self photo on a patterned backdrop —
 * and it reads the headcount bands as bands rather than cumulatively: the PDF's
 * "MIDI 1-4 ORANG" is "3-4 ORANG" here, which is how the packages are actually
 * priced, since a pair booking MIDI would be paying MINI's price for MINI's room.
 *
 * Where it agrees with the PDF against the owner, the owner wins. MINI's session
 * is the only case, and both stale sources say fifteen minutes because the booking
 * page was configured from the PDF — one source repeated, not two agreeing.
 */
const BOOKING = "studiobykami-{self,photobox}.youcanbook.me, read 2026-08-10";

/**
 * The second price list, for work away from the studio — graduations, aqiqah,
 * running events, yearbooks. Same standing as the indoor one: the owner's own
 * document, gitignored, never confirmed as current.
 */
const OUTDOOR_PDF = "refs/PRICE LIST DILUAR STUDIO.pdf (owner PDF, not owner-confirmed)";

/**
 * Why the four packages below carry no price.
 *
 * Not a missing number: the PDF's figures are still in `OUTDOOR_PDF` and the
 * booking API still holds them. The owner withdrew them from the pages on
 * 2026-08-20 because this work is quoted per job — location, duration and
 * headcount all move it — and a headline rate a customer arrives holding is a
 * rate the studio then has to argue out of.
 *
 * `blocked` because that is the status that does not render and does not
 * publish, which is what withheld needs. It does mean these show up in
 * `gaps()` as if they were unconfirmed facts; they are the one kind of blocked
 * fact that no amount of asking the owner will close.
 */
const QUOTED_PER_JOB =
  "Withheld on purpose (owner, 2026-08-20) — quoted per job. The figure is still in " +
  "refs/PRICE LIST DILUAR STUDIO.pdf and in the booking API's seed.";

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
  shortName: "studio",
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
    // The pin the studio itself sent customers to, off the booking pages'
    // location field. Two pins were configured, one per calendar, and they are
    // different places: this is the self-photo one. The photobox pin is
    // maps.app.goo.gl/Wvj9tN5ymvmdt9mj8, which is worth knowing because two pins
    // is the strongest evidence in any source that the two are separate rooms.
    //
    // A shortlink, which is not ideal — it resolves through Google and cannot be
    // read to check the coordinates. It is what the studio published, so it is
    // what a customer following directions actually used.
    mapsUrl: unverified("https://maps.app.goo.gl/FPPUVrMcq2NRFAVS8", BOOKING),
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
    // Shared with the other three properties — see verticals/contact.ts. This is
    // where the number came from, so the shared constant carries this PDF as its
    // source.
    whatsapp: houseWhatsapp,
    // The strongest source any fact here has: the page is built in this
    // repository and deployed by its own CI, so this is read off the thing
    // itself rather than off a document about it.
    //
    // It replaces the two YouCanBook.me calendars, whose URLs were never
    // recorded and which are the reason this was blocked. Those presented six
    // choices over one shared availability pool; this one knows the studio runs
    // photobox and self-photo in parallel.
    bookingUrl: verified(
      "/booking",
      "sites/studio/src/pages/booking.astro — in-house booking, live since 2026-08-10",
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
     * Three, chosen by the owner, down from twelve. Each embed is an iframe and
     * a script, so twelve of them was the heaviest thing on the page by a wide
     * margin, spent on content that is not indexable as this page's own. Three
     * is what the TikTok section carries and it is enough to show the account is
     * active, which is the whole job.
     *
     * These have captions where the twelve did not, and the difference is that
     * these have been looked at. Instagram serves the signup wall to a logged-out
     * browser, but `/embed/captioned/` is public — it renders the post and its
     * caption, which is how each of these was read and how each was checked to
     * be portrait and to be this account's.
     *
     * Trimmed to the sentence a human wrote, the same rule the videos below
     * follow: the hashtag block, the booking links and the repeated address are
     * noise in a fallback line. The first post's caption also sells a
     * differently-named studio at this address — an older post, before the
     * rename — and the trim drops that too rather than putting another brand's
     * name on this page.
     */
    posts: verified(
      [
        {
          kind: "p",
          shortcode: "C_ZhSLqSUXx",
          caption: "Pengen pas foto tapi juga bisa sambil seru-seruan di self photo studio?",
        },
        {
          kind: "p",
          shortcode: "DCOuh9ZSz_Y",
          caption: "Abadikan moment bersama keluarga tercinta di studio by KAMI.",
        },
        {
          kind: "p",
          shortcode: "DJB9l4DPRN0",
          caption:
            "Kebahagiaan adalah momen spesial yang akan selalu berkesan di hati, diabadikan melalui foto bersama studio by KAMI.",
        },
      ],
      "owner, 2026-08-09 — permalinks supplied directly; each rendered via /embed/captioned/ and its caption read back",
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
      headcount: unverified({ min: 1, max: 2 }, `${BOOKING} — "1-2 ORANG"; the PDF agrees here`),
    },
    {
      id: "midi",
      name: "MIDI",
      serviceLine: "self-photo",
      orderIndex: 1,
      priceIDR: unverified(70_000, PDF),
      durationMinutes: unverified(20, PDF),
      printsIncluded: unverified(2, PDF),
      headcount: unverified({ min: 3, max: 4 }, `${BOOKING} — "3-4 ORANG"; the PDF read 1-4 cumulatively`),
    },
    {
      id: "maxi",
      name: "MAXI",
      serviceLine: "self-photo",
      orderIndex: 2,
      priceIDR: unverified(95_000, PDF),
      durationMinutes: unverified(20, PDF),
      printsIncluded: unverified(2, PDF),
      headcount: unverified({ min: 5, max: 6 }, `${BOOKING} — "5-6 ORANG"; the PDF read 1-6 cumulatively`),
    },
    {
      id: "big-maxi",
      name: "BIG MAXI",
      serviceLine: "self-photo",
      orderIndex: 3,
      priceIDR: unverified(165_000, PDF),
      durationMinutes: unverified(25, PDF),
      printsIncluded: unverified(3, PDF),
      headcount: unverified({ min: 7, max: 10 }, `${BOOKING} — "7-10 ORANG"; the PDF read 1-10 cumulatively`),
    },
    /*
     * The two take counts here are the owner's, given on 2026-08-20, and they
     * are the only numbers in this file that beat the PDF on their own subject.
     *
     * A pas foto is not sold by the minute. Every other package rents a room for
     * a length of time; this one is a face against a background, and what the
     * studio actually promises is how many tries you get — which is why the
     * booking page prints takes where it prints minutes everywhere else. The
     * PDF's "maksimal 15x take" was simply out of date.
     *
     * They sit in `description` because that is where the PDF put them and there
     * is no takes field to move them to. Adding one would mean a schema change
     * here and a matching override on the booking page, which reads its packages
     * from the API rather than from this file.
     */
    {
      id: "pas-foto",
      name: "Pas Foto (close up)",
      serviceLine: "pas-foto",
      description: "Maksimal 10x take, 1 print 4R (bisa custom ukuran), 1 file edit.",
      orderIndex: 4,
      priceIDR: unverified(50_000, `${PDF} — per orang`),
      printsIncluded: unverified(1, PDF),
      headcount: unverified({ min: 1, max: 1 }, PDF),
    },
    {
      id: "marry-me",
      name: "Marry Me (pas foto)",
      serviceLine: "pas-foto",
      description: "Maksimal 20x take, 1 background, 2 print 4R, 6 file edit.",
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

    /*
     * Self photo on a patterned backdrop, and the clearest thing the booking
     * pages had that no document here did.
     *
     * It is a separate line from the four plain-backdrop packages rather than an
     * option on them, because it is priced as one: MOTIF MIDI takes one to four
     * people for 80K where plain MIDI takes three to four for 70K, so the pair
     * who would pay 45K on plain pay 80K on motif. Folding it into a backdrop
     * choice would put a price on a radio button.
     */
    {
      id: "motif-midi",
      name: "MOTIF MIDI",
      serviceLine: "self-photo",
      description: "Background bermotif, 2 print 4R.",
      orderIndex: 7,
      priceIDR: unverified(80_000, BOOKING),
      durationMinutes: unverified(20, BOOKING),
      printsIncluded: unverified(2, BOOKING),
      headcount: unverified({ min: 1, max: 4 }, BOOKING),
    },
    {
      id: "motif-family",
      name: "MOTIF FAMILY",
      serviceLine: "self-photo",
      description: "Background bermotif, 2 print 4R.",
      orderIndex: 8,
      priceIDR: unverified(120_000, BOOKING),
      durationMinutes: unverified(20, BOOKING),
      printsIncluded: unverified(2, BOOKING),
      headcount: unverified({ min: 5, max: 8 }, BOOKING),
    },
    {
      id: "motif-squad",
      name: "MOTIF SQUAD",
      serviceLine: "self-photo",
      description: "Background bermotif, 3 print 4R.",
      orderIndex: 9,
      priceIDR: unverified(180_000, BOOKING),
      durationMinutes: unverified(25, BOOKING),
      printsIncluded: unverified(3, BOOKING),
      headcount: unverified({ min: 9, max: 12 }, BOOKING),
    },

    /*
     * The photobox: a booth, not a room. Three backdrops at two prices, ten
     * minutes each, priced per person with two strips included per head.
     *
     * Filed under `studio` rather than `booth`, which is the one editorial call
     * in this block. `booth` is the mobile photobooth rented for a wedding — a
     * whole-event hire with a crew — and this is a machine in the Jajag shop that
     * a walk-in uses for ten minutes. Same word, different business, and the
     * customer deciding between this and a self-photo room is on this page.
     */
    {
      id: "photobox-y2k",
      name: "Photobox Y2K",
      serviceLine: "photobox",
      description: "10 menit, gratis 2 strip foto 2R tiap orang.",
      orderIndex: 10,
      priceIDR: unverified(30_000, `${BOOKING} — per orang`),
      durationMinutes: unverified(10, BOOKING),
      printsIncluded: unverified(2, `${BOOKING} — per orang`),
      headcount: unverified({ min: 1, max: 5 }, BOOKING),
    },
    {
      id: "photobox-vintage",
      name: "Photobox Vintage",
      serviceLine: "photobox",
      description: "10 menit, gratis 2 strip foto 2R tiap orang.",
      orderIndex: 11,
      priceIDR: unverified(25_000, `${BOOKING} — per orang`),
      durationMinutes: unverified(10, BOOKING),
      printsIncluded: unverified(2, `${BOOKING} — per orang`),
      headcount: unverified({ min: 1, max: 5 }, BOOKING),
    },
    {
      id: "photobox-maroon",
      name: "Photobox Maroon",
      serviceLine: "photobox",
      description: "10 menit, gratis 2 strip foto 2R tiap orang.",
      orderIndex: 12,
      priceIDR: unverified(25_000, `${BOOKING} — per orang`),
      durationMinutes: unverified(10, BOOKING),
      printsIncluded: unverified(2, `${BOOKING} — per orang`),
      headcount: unverified({ min: 1, max: 5 }, BOOKING),
    },

    /*
     * Work away from the studio, from its own price list.
     *
     * Priced in hours, delivered as a Drive folder, and at the customer's
     * location — which is why these carry a different `serviceLine` from the
     * in-studio work and why the booking system gives them a resource of their
     * own. Nothing here says how much travel time to leave between two of them;
     * that is an open question for the owner rather than a number to invent.
     */
    {
      id: "fotografer-1-jam",
      name: "Fotografer 1 Jam",
      serviceLine: "outdoor-photographer",
      description: "Maksimal 1 jam, semua file diedit, gratis 2 print 4R.",
      orderIndex: 13,
      priceIDR: blocked(QUOTED_PER_JOB),
      durationMinutes: unverified(60, OUTDOOR_PDF),
      printsIncluded: unverified(2, OUTDOOR_PDF),
    },
    {
      id: "fotografer-15-jam",
      name: "Fotografer 1,5 Jam",
      serviceLine: "outdoor-photographer",
      description: "Maksimal 1,5 jam, semua file diedit, gratis 4 print 4R.",
      orderIndex: 14,
      priceIDR: blocked(QUOTED_PER_JOB),
      durationMinutes: unverified(90, OUTDOOR_PDF),
      printsIncluded: unverified(4, OUTDOOR_PDF),
    },
    {
      id: "fotografer-3-jam",
      name: "Fotografer 3 Jam",
      serviceLine: "outdoor-photographer",
      description: "Maksimal 3 jam, semua file diedit, gratis 1 print 10R berpigura.",
      orderIndex: 15,
      priceIDR: blocked(QUOTED_PER_JOB),
      durationMinutes: unverified(180, OUTDOOR_PDF),
      printsIncluded: unverified(1, OUTDOOR_PDF),
    },
    {
      id: "videografer-3-jam",
      name: "Videografer 3 Jam",
      serviceLine: "videographer",
      description: "Maksimal 3 jam, hasil edit 2–4 menit.",
      orderIndex: 16,
      priceIDR: blocked(QUOTED_PER_JOB),
      durationMinutes: unverified(180, OUTDOOR_PDF),
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
      // Deliberately still blocked. The booking calendar accepts a slot half an
      // hour out, which says how late you may book — and nothing about whether
      // somebody who walks in without booking is served. Answering the second
      // from the first would be a guess printed as a fact.
      answer: blocked(
        "Walk-in policy still unknown. The booking calendar's 30-minute minimum notice says how late a slot can be taken, not whether an unbooked visitor is turned away.",
      ),
    },
    {
      id: "cara-booking",
      question: "Bagaimana cara booking?",
      topic: "booking",
      answer: unverified(
        "Pilih paket dan jam yang tersedia di halaman booking, isi nama dan nomor WhatsApp, lalu jadwalmu langsung terkonfirmasi. Bisa juga chat admin lewat WhatsApp.",
        `${BOOKING} — the form asks for Nama, No WhatsApp, Alamat Email and Jumlah Orang`,
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
      // The one policy the booking form stated outright, and it had to be ticked
      // before a booking went through. Recorded as the studio worded it: the
      // charge covers arriving late as well as cancelling or moving a slot, which
      // is not what most people assume.
      answer: unverified(
        "Bisa, asal lebih dari 6 jam sebelum jadwal. Terlambat, batal, atau reschedule kurang dari 6 jam sebelum jadwal dikenakan denda Rp20.000.",
        `${BOOKING} — "Terlambat, cancel dan reschedule kurang dari H-6 jam dikenakan denda sebesar 20k"`,
      ),
    },
    {
      id: "bawa-properti",
      question: "Boleh bawa properti sendiri?",
      topic: "policy",
      // The booking terms make a customer liable for damaging the studio's own
      // props, which is a different question from whether they may bring theirs.
      answer: blocked(
        "Props policy still unknown. The booking terms cover damage to the studio's property, not whether a customer may bring their own.",
      ),
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
