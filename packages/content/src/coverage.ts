import type { Vertical } from "./schema.ts";
import type { Sourced } from "./sourced.ts";

export type Gap = {
  vertical: Vertical["id"];
  field: string;
  status: "unverified" | "blocked";
  detail: string;
};

const inspect = <T>(
  vertical: Vertical["id"],
  field: string,
  s: Sourced<T> | undefined,
  out: Gap[],
): void => {
  if (!s) return;
  if (s.status === "verified") return;
  out.push({
    vertical,
    field,
    status: s.status,
    detail: s.status === "blocked" ? s.note : `unverified — ${s.source}`,
  });
};

/**
 * Everything still standing between the build and a publishable launch.
 *
 * This replaces `design/assets-needed.md` as the source of truth: that document
 * is maintained by hand and drifts, this is derived from the same data the sites
 * render, so it cannot disagree with what actually ships.
 */
export const gaps = (verticals: Vertical[]): Gap[] => {
  const out: Gap[] = [];

  for (const v of verticals) {
    inspect(v.id, "brand.logoSvg", v.brand.logoSvg, out);
    inspect(v.id, "brand.accentColor", v.brand.accentColor, out);

    if (v.nap) {
      inspect(v.id, "nap.address", v.nap.address, out);
      inspect(v.id, "nap.mapsUrl", v.nap.mapsUrl, out);
      inspect(v.id, "nap.openingHours", v.nap.openingHours, out);
      inspect(v.id, "nap.whatsapp", v.nap.whatsapp, out);
      inspect(v.id, "nap.bookingUrl", v.nap.bookingUrl, out);
    }

    if (v.social) {
      inspect(v.id, "social.instagram", v.social.instagram, out);
      inspect(v.id, "social.tiktok", v.social.tiktok, out);
      inspect(v.id, "social.posts", v.social.posts, out);
    }

    for (const o of v.offerings) {
      inspect(v.id, `offering[${o.id}].priceIDR`, o.priceIDR, out);
      inspect(v.id, `offering[${o.id}].durationMinutes`, o.durationMinutes, out);
      inspect(v.id, `offering[${o.id}].headcount`, o.headcount, out);
      inspect(v.id, `offering[${o.id}].printsIncluded`, o.printsIncluded, out);
    }

    for (const b of v.backdrops) inspect(v.id, `backdrop[${b.id}].image`, b.image, out);
    for (const p of v.promos) inspect(v.id, `promo[${p.id}].mechanic`, p.mechanic, out);
    for (const f of v.faqs) inspect(v.id, `faq[${f.id}].answer`, f.answer, out);
  }

  return out;
};

/** True when every fact in these verticals is owner-confirmed. */
export const launchReady = (verticals: Vertical[]): boolean =>
  gaps(verticals).length === 0;

export const formatGaps = (all: Gap[]): string => {
  if (all.length === 0) return "All facts owner-verified. Launch gate open.\n";

  const byVertical = new Map<string, Gap[]>();
  for (const g of all) {
    const list = byVertical.get(g.vertical) ?? [];
    list.push(g);
    byVertical.set(g.vertical, list);
  }

  const blocked = all.filter((g) => g.status === "blocked").length;
  const lines: string[] = [
    `${all.length} fact(s) not publishable — ${blocked} blocked, ${all.length - blocked} unverified.`,
    "",
  ];

  for (const [vertical, list] of byVertical) {
    lines.push(`${vertical} (${list.length})`);
    for (const g of list) {
      lines.push(`  [${g.status.padEnd(10)}] ${g.field} — ${g.detail}`);
    }
    lines.push("");
  }

  return lines.join("\n");
};
