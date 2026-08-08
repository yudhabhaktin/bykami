/**
 * Opening hours, as a line a customer reads.
 *
 * They are stored in schema.org's form — "Mo-Su 09:00-21:00" — because that is
 * what `LocalBusiness.openingHours` has to carry, and that form is the whole
 * reason this file exists: it is machine syntax, and the footer of a shop's
 * website is not the place to show a search engine's serialisation to somebody
 * deciding whether to drive over. Storing a second, human copy beside it would
 * be storing the same fact twice and inviting the two to disagree, so the human
 * one is derived.
 *
 * Anything unrecognised comes back unchanged. An unusual entry should degrade to
 * its raw text rather than be dropped, which would silently under-report when
 * the studio is open, or guessed at, which would misreport it.
 */
const DAY: Record<string, string> = {
  Mo: "Senin",
  Tu: "Selasa",
  We: "Rabu",
  Th: "Kamis",
  Fr: "Jumat",
  Sa: "Sabtu",
  Su: "Minggu",
};

/** One `openingHours` entry: a day or day range, then a time range. */
const SPEC =
  /^(Mo|Tu|We|Th|Fr|Sa|Su)(?:-(Mo|Tu|We|Th|Fr|Sa|Su))?\s+(\d{2}):(\d{2})-(\d{2}):(\d{2})$/;

const one = (spec: string): string => {
  const m = SPEC.exec(spec.trim());
  if (!m) return spec;

  const from = m[1] as string;
  const to = m[2];
  const [openH, openM, closeH, closeM] = [m[3], m[4], m[5], m[6]] as string[];

  const days =
    to === undefined
      ? DAY[from]
      : from === "Mo" && to === "Su"
        ? "Setiap hari"
        : `${DAY[from]}–${DAY[to]}`;

  // Indonesian writes a time with a full stop, and a range with an en dash.
  return `${days} ${openH}.${openM}–${closeH}.${closeM}`;
};

export const openingHoursText = (hours: string[]): string => hours.map(one).join(", ");
