import { vertical, type Vertical } from "../schema.ts";
import { booth } from "./booth.ts";
import { dimsamcong } from "./dimsamcong.ts";
import { root } from "./root.ts";
import { studio } from "./studio.ts";

/**
 * Parsed at module load, so malformed content is a build failure rather than a
 * runtime surprise on a deployed page.
 */
export const verticals: Vertical[] = [root, studio, booth, dimsamcong].map((v) =>
  vertical.parse(v),
);

export const byId = (id: Vertical["id"]): Vertical => {
  const found = verticals.find((v) => v.id === id);
  if (!found) throw new Error(`Unknown vertical: ${id}`);
  return found;
};
