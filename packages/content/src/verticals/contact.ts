import type { Sourced } from "../sourced.ts";
import { unverified } from "../sourced.ts";

/**
 * One WhatsApp line for the by KAMI properties — the root, the studio, and the
 * booth. Dimsamcong is deliberately not on it: it keeps the line its own
 * Instagram bio publishes, which is also the vertical whose brand by KAMI does
 * not own.
 *
 * Held here rather than typed into three vertical files because the sharing is
 * itself the fact the owner settled (2026-08-09): changing the number is one
 * edit, and the three cannot silently drift onto different lines.
 *
 * Still `unverified`, and the distinction matters — what the owner confirmed is
 * *which* line answers, not the digits. Those trace to the studio price list,
 * which prints "0811-3777-10": short for an ID mobile and possibly clipped in
 * the PDF. So the button renders, and the number stays out of `telephone` in
 * structured data until someone reads it off a handset. Promoting this one call
 * to `verified` publishes it on all three at once.
 */
export const houseWhatsapp: Sourced<string> = unverified(
  "62811377710",
  'refs/Price LIst Studio Indoor.pdf — printed as "0811-3777-10", which is short for an ID mobile number and may be clipped; owner, 2026-08-09 — confirmed the root, studio, and booth share this line',
);
