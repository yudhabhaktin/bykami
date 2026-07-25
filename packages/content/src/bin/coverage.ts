import { formatGaps, gaps } from "../coverage.ts";
import { verticals } from "../verticals/index.ts";

const all = gaps(verticals);
process.stdout.write(formatGaps(all));
process.exit(all.length === 0 ? 0 : 1);
