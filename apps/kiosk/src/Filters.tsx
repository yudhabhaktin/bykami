import type { Filter } from "./api";

/** The id nothing is filtered with, matching compose.NoFilter. */
export const NO_FILTER = "asli";

/** The CSS filter value for a filter id, or none for the identity filter. */
export function filterCSS(id: string): string | undefined {
  return id && id !== NO_FILTER ? `url(#${defID(id)})` : undefined;
}

function defID(id: string): string {
  return `bykami-filter-${id}`;
}

/**
 * The filter definitions, mounted once.
 *
 * These are SVG filters rather than CSS `filter: sepia()` because the printed
 * sheet is filtered in Go from a colour matrix, and an feColorMatrix takes that
 * exact matrix. The screen and the paper therefore run the same arithmetic on
 * the same numbers — the agent serves them — instead of two approximations of
 * the same idea.
 *
 * `color-interpolation-filters="sRGB"` is load-bearing and not a default: SVG
 * filters operate in linear RGB unless told otherwise, and Go applies the
 * matrix to sRGB values straight out of the JPEG. Without this attribute every
 * filter would be visibly darker on paper than on screen, which is exactly the
 * class of bug serving one matrix was meant to remove.
 */
export function FilterDefs({ filters }: { filters: Filter[] }) {
  return (
    <svg aria-hidden="true" focusable="false" style={{ position: "absolute", width: 0, height: 0 }}>
      <defs>
        {filters
          .filter((f) => f.matrix)
          .map((f) => (
            <filter key={f.id} id={defID(f.id)} colorInterpolationFilters="sRGB">
              <feColorMatrix type="matrix" values={f.matrix!.join(" ")} />
            </filter>
          ))}
      </defs>
    </svg>
  );
}

/**
 * The picker.
 *
 * Each swatch is the customer's own first photo with the filter on it, because
 * a row of named buttons asks somebody to imagine what "Pudar" looks like. With
 * no photo yet it falls back to a coloured tile, which still shows the shift.
 */
export function FilterPicker({
  filters,
  value,
  onChange,
  sample,
}: {
  filters: Filter[];
  value: string;
  onChange: (id: string) => void;
  sample?: string;
}) {
  return (
    <div className="filters" role="group" aria-label="Filter foto">
      {filters.map((f) => (
        <button
          key={f.id}
          className="filter"
          aria-pressed={f.id === value}
          onClick={() => onChange(f.id)}
        >
          <span className="filter-swatch">
            {sample ? (
              <img src={sample} alt="" style={{ filter: filterCSS(f.id) }} />
            ) : (
              <span className="filter-blank" style={{ filter: filterCSS(f.id) }} />
            )}
          </span>
          <span className="filter-name">{f.name}</span>
        </button>
      ))}
    </div>
  );
}
