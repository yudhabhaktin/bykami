import type { Vertical } from "../schema.ts";
import { blocked, unverified, verified } from "../sourced.ts";
import { houseWhatsapp } from "./contact.ts";

const PDF = "refs/PRICE LIST PHOTOBOOTH.pdf (owner PDF, not owner-confirmed)";

/**
 * booth by KAMI — mobile photobooth hire for schools, weddings, and events.
 *
 * Promoted from a service line inside studio to its own vertical: it has its own
 * Instagram account, its own catalogue, and it travels to the customer across
 * three regencies rather than operating from the Jajag address.
 */
export const booth: Vertical = {
  id: "booth",
  displayName: "booth by KAMI",
  hostname: "booth.bykami.id",
  tagline: "Photobooth untuk sekolah, wedding, dan event",
  description:
    "Sewa photobooth untuk acara sekolah, pernikahan, dan event di Banyuwangi, Jember, dan Bondowoso. Unlimited photo, limited print, atau unlimited print — lengkap dengan backdrop dan desain frame.",
  schemaType: "PhotographyBusiness",
  indexable: true,

  nap: {
    legalName: "booth by KAMI",
    displayName: "booth by KAMI",
    address: unverified(
      {
        streetAddress: "Jalan Yos Sudarso, Jajag Barat (Hotel Surya)",
        addressLocality: "Jajag, Gambiran",
        addressRegion: "Banyuwangi, Jawa Timur",
        addressCountry: "ID" as const,
      },
      "Shared base with studio by KAMI; booth itself travels to the customer",
    ),
    mapsUrl: blocked("No Google Maps link in any source."),
    openingHours: blocked("Operating hours unknown; bookings are per-event."),
    // The photobooth PDF shows no number of its own, and the owner has now
    // answered the question that left this blocked: it shares the studio line.
    whatsapp: houseWhatsapp,
    bookingUrl: blocked("No booking URL recorded for the photobooth service."),
  },

  social: {
    instagram: unverified(
      "boothbykami",
      "design/direction.md — read out of the studio bio as a sister account, never opened",
    ),
    tiktok: blocked("Owner has not said whether a TikTok account exists."),
    /*
     * Supplied for this vertical specifically, rather than split off the studio
     * set: the booth is the thing being sold here, and a photobooth post on the
     * studio page would be advertising a different product to someone reading
     * about studio hire.
     */
    posts: verified(
      [
        { kind: "p", shortcode: "DbastU-vOKa" },
        { kind: "p", shortcode: "DbLXcJIgcpz" },
        { kind: "p", shortcode: "DbLWz9SBm-k" },
        { kind: "p", shortcode: "DbLWhrjgdUl" },
        { kind: "p", shortcode: "Da91AfggUM_" },
        { kind: "p", shortcode: "DaKfMk7AVpe" },
        { kind: "p", shortcode: "DZcqWOwh2nL" },
        { kind: "p", shortcode: "DZcaIkQAcp8" },
        { kind: "p", shortcode: "DZY8fBKgZRj" },
        { kind: "p", shortcode: "DZQCD2qAesG" },
        { kind: "p", shortcode: "DZNP3G4AfJi" },
        { kind: "p", shortcode: "DYylxEkgUmF" },
      ],
      "owner, 2026-08-08 and 2026-08-09 — permalinks supplied directly; each one checked to resolve",
    ),
  },

  brand: {
    logoSvg: blocked("Original vector never supplied. See design/assets-needed.md."),
    accentColor: blocked("Derived from the logo vector, which is blocked."),
  },

  offerings: [
    {
      id: "unlimited-photo-2j",
      name: "Unlimited Photo — 2 jam",
      serviceLine: "photobox",
      description: "File only. Termasuk backdrop standar dan desain frame.",
      orderIndex: 0,
      priceIDR: unverified(1_000_000, PDF),
      durationMinutes: unverified(120, PDF),
    },
    {
      id: "unlimited-photo-3j",
      name: "Unlimited Photo — 3 jam",
      serviceLine: "photobox",
      description: "File only. Termasuk backdrop standar dan desain frame.",
      orderIndex: 1,
      priceIDR: unverified(1_200_000, PDF),
      durationMinutes: unverified(180, PDF),
    },
    {
      id: "unlimited-photo-4j",
      name: "Unlimited Photo — 4 jam",
      serviceLine: "photobox",
      description: "File only. Termasuk backdrop standar dan desain frame.",
      orderIndex: 2,
      priceIDR: unverified(1_400_000, PDF),
      durationMinutes: unverified(240, PDF),
    },
    {
      id: "limited-print-200",
      name: "Limited Print — 200 strip",
      serviceLine: "photobox",
      description: "200 cetak strip 2R dalam 3 jam. Termasuk backdrop standar dan desain frame.",
      orderIndex: 3,
      priceIDR: unverified(1_500_000, PDF),
      durationMinutes: unverified(180, PDF),
      printsIncluded: unverified(200, PDF),
    },
    {
      id: "limited-print-300",
      name: "Limited Print — 300 strip",
      serviceLine: "photobox",
      description: "300 cetak strip 2R dalam 3 jam. Termasuk backdrop standar dan desain frame.",
      orderIndex: 4,
      priceIDR: unverified(1_850_000, PDF),
      durationMinutes: unverified(180, PDF),
      printsIncluded: unverified(300, PDF),
    },
    {
      id: "limited-print-400",
      name: "Limited Print — 400 strip",
      serviceLine: "photobox",
      description: "400 cetak strip 2R dalam 3 jam. Termasuk backdrop standar dan desain frame.",
      orderIndex: 5,
      priceIDR: unverified(2_100_000, PDF),
      durationMinutes: unverified(180, PDF),
      printsIncluded: unverified(400, PDF),
    },
    {
      id: "unlimited-print-2j",
      name: "Unlimited Print — 2 jam",
      serviceLine: "photobox",
      description: "Cetak tanpa batas, frame 4R/strip. Termasuk backdrop standar dan desain frame.",
      orderIndex: 6,
      priceIDR: unverified(1_950_000, PDF),
      durationMinutes: unverified(120, PDF),
    },
    {
      id: "unlimited-print-3j",
      name: "Unlimited Print — 3 jam",
      serviceLine: "photobox",
      description: "Cetak tanpa batas, frame 4R/strip. Termasuk backdrop standar dan desain frame.",
      orderIndex: 7,
      priceIDR: unverified(2_400_000, PDF),
      durationMinutes: unverified(180, PDF),
    },
    {
      id: "unlimited-print-4j",
      name: "Unlimited Print — 4 jam",
      serviceLine: "photobox",
      description: "Cetak tanpa batas, frame 4R/strip. Termasuk backdrop standar dan desain frame.",
      orderIndex: 8,
      priceIDR: unverified(2_850_000, PDF),
      durationMinutes: unverified(240, PDF),
    },
    {
      id: "addon-cover-frame",
      name: "Add-on: cover frame",
      serviceLine: "photobox",
      description: "Cover atau bingkai untuk hasil cetak, 200 pcs ukuran 2R.",
      orderIndex: 9,
      priceIDR: unverified(200_000, PDF),
    },
    {
      id: "addon-creative-space",
      name: "Add-on: creative space",
      serviceLine: "photobox",
      description: "Gantungan kunci untuk 50 pcs.",
      orderIndex: 10,
      priceIDR: unverified(200_000, PDF),
    },
  ],

  backdrops: [],

  promos: [],

  faqs: [
    {
      id: "area-layanan",
      question: "Photobooth-nya melayani daerah mana saja?",
      topic: "location",
      answer: unverified("Banyuwangi, Jember, dan Bondowoso.", `${PDF} — cover page`),
    },
    {
      id: "jenis-acara",
      question: "Cocok untuk acara apa saja?",
      topic: "session",
      answer: unverified(
        "Acara sekolah, pernikahan, dan event seperti pameran, konser, ulang tahun, sampai acara perusahaan.",
        `${PDF} — "OUR SERVICES"`,
      ),
    },
    {
      id: "beda-paket",
      question: "Apa beda Unlimited Photo, Limited Print, dan Unlimited Print?",
      topic: "pricing",
      answer: unverified(
        "Unlimited Photo hanya file digital. Limited Print memberi jumlah cetak strip 2R yang tetap. Unlimited Print mencetak tanpa batas dengan frame 4R/strip.",
        PDF,
      ),
    },
    {
      id: "backdrop-termasuk",
      question: "Backdrop dan desain frame sudah termasuk?",
      topic: "pricing",
      answer: unverified(
        "Sudah. Semua paket termasuk backdrop standar dan desain frame.",
        `${PDF} — "*Free Backdrop standart dan Desain Frame"`,
      ),
    },
    {
      id: "tambah-jam",
      question: "Bisa tambah durasi di tempat?",
      topic: "pricing",
      answer: unverified(
        "Bisa. Tambahan file saja Rp 300.000 per jam. Untuk Unlimited Print, tambahan 1 jam Rp 650.000.",
        PDF,
      ),
    },
    {
      id: "tambah-cetak",
      question: "Kalau cetakan habis di tengah acara?",
      topic: "pricing",
      answer: unverified(
        "Pada paket Limited Print ada tambahan di tempat: 100 cetak 2R atau selama 30 menit seharga Rp 450.000.",
        PDF,
      ),
    },
    {
      id: "backdrop-custom",
      question: "Bisa pakai backdrop custom sesuai tema acara?",
      topic: "session",
      answer: blocked("Custom backdrop availability and pricing unknown."),
    },
    {
      id: "berapa-operator",
      question: "Ada operator yang menjaga photobooth?",
      topic: "session",
      answer: blocked("Staffing arrangement unknown."),
    },
    {
      id: "luas-tempat",
      question: "Butuh ruang seluas apa untuk photobooth?",
      topic: "session",
      answer: blocked("Space and power requirements unknown."),
    },
    {
      id: "dp-booth",
      question: "Berapa DP untuk booking photobooth?",
      topic: "booking",
      answer: blocked("Deposit terms for event bookings unknown."),
    },
    {
      id: "booking-jauh",
      question: "Berapa jauh hari sebelumnya harus booking?",
      topic: "booking",
      answer: blocked("Lead time unknown."),
    },
    {
      id: "biaya-luar-kota",
      question: "Ada biaya transport untuk luar kota?",
      topic: "pricing",
      answer: blocked(
        "Travel surcharge outside Banyuwangi is not stated, though three regencies are served.",
      ),
    },
    {
      id: "file-booth",
      question: "File hasil photobooth dikirim lewat apa?",
      topic: "results",
      answer: blocked("Delivery method and window unknown."),
    },
  ],
};
