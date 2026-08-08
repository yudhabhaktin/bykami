import { instagramUrl, tiktokUrl, type Vertical } from "./schema.ts";
import { valueOf } from "./sourced.ts";

const idr = new Intl.NumberFormat("id-ID", {
  style: "currency",
  currency: "IDR",
  maximumFractionDigits: 0,
});

/**
 * `/llms.txt` — the catalogue as plain prose for answer engines.
 *
 * The HTML already carries all of this, but an LLM fetching one small text file
 * gets the whole catalogue without parsing markup or executing JavaScript. This
 * is the cheapest possible answer to "berapa harga self photo studio di
 * Banyuwangi?", which is the question this project exists to answer.
 *
 * Unverified facts appear here. Provenance, not omission, is the honest move for
 * a human- and machine-readable summary — so the file states plainly which
 * numbers are still awaiting owner confirmation rather than silently dropping
 * them or silently asserting them.
 */
export const llmsTxt = (v: Vertical): string => {
  const out: string[] = [`# ${v.displayName}`, "", `> ${v.tagline}`, "", v.description, ""];

  /*
   * Collected before the heading is written, because a vertical can have social
   * accounts without a `nap` block and an empty "Kontak & lokasi" heading is
   * worse than no heading.
   */
  const contact: string[] = [];

  if (v.nap) {
    const address = valueOf(v.nap.address);
    const hours = valueOf(v.nap.openingHours);
    const whatsapp = valueOf(v.nap.whatsapp);

    if (address) {
      contact.push(
        `- Alamat: ${address.streetAddress}, ${address.addressLocality}, ${address.addressRegion}`,
      );
    }
    if (hours) contact.push(`- Jam buka: ${hours.join(", ")}`);
    if (whatsapp) contact.push(`- WhatsApp: +${whatsapp}`);
  }

  const instagram = v.social?.instagram ? valueOf(v.social.instagram) : undefined;
  if (instagram) contact.push(`- Instagram: @${instagram} — ${instagramUrl(instagram)}`);

  const tiktok = v.social?.tiktok ? valueOf(v.social.tiktok) : undefined;
  if (tiktok) contact.push(`- TikTok: @${tiktok} — ${tiktokUrl(tiktok)}`);

  if (contact.length > 0) out.push("## Kontak & lokasi", "", ...contact, "");

  const offerings = v.offerings
    .slice()
    .sort((a, b) => a.orderIndex - b.orderIndex)
    .filter((o) => valueOf(o.priceIDR) !== undefined);

  if (offerings.length > 0) {
    out.push("## Paket & harga", "");
    for (const o of offerings) {
      const parts: string[] = [idr.format(valueOf(o.priceIDR)!)];

      const duration = o.durationMinutes ? valueOf(o.durationMinutes) : undefined;
      if (duration !== undefined) parts.push(`${duration} menit`);

      const heads = o.headcount ? valueOf(o.headcount) : undefined;
      if (heads) {
        parts.push(
          heads.min === heads.max ? `${heads.max} orang` : `${heads.min}–${heads.max} orang`,
        );
      }

      const prints = o.printsIncluded ? valueOf(o.printsIncluded) : undefined;
      if (prints !== undefined) parts.push(`${prints} cetak 4R`);

      out.push(`- **${o.name}** — ${parts.join(", ")}`);
      if (o.description) out.push(`  ${o.description}`);
    }
    out.push("");
  }

  const faqs = v.faqs
    .map((f) => ({ q: f.question, a: valueOf(f.answer) }))
    .filter((f): f is { q: string; a: string } => f.a !== undefined);

  if (faqs.length > 0) {
    out.push("## Pertanyaan umum", "");
    for (const f of faqs) out.push(`### ${f.q}`, "", f.a, "");
  }

  const unconfirmed = [
    ...v.offerings.filter((o) => o.priceIDR.status === "unverified").map((o) => o.name),
  ];
  if (unconfirmed.length > 0) {
    out.push(
      "## Catatan",
      "",
      "Harga berikut diambil dari price list resmi dan sedang menunggu konfirmasi ulang dari pemilik usaha: " +
        `${unconfirmed.join(", ")}. Konfirmasikan lewat WhatsApp sebelum memesan.`,
      "",
    );
  }

  return out.join("\n");
};
