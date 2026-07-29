import { api, type Photo, type Template } from "./api";
import { filterCSS } from "./Filters";

/**
 * The sheet as it will print, composited in the browser.
 *
 * The same three layers compose.Sheet draws, in the same order — background,
 * photos, overlay — positioned as percentages of the template's own 300 dpi
 * geometry. Nothing here restates that geometry, so the preview cannot drift
 * away from the printed sheet without the manifest changing.
 *
 * `object-fit: cover` is the CSS equivalent of compose.drawCover: fill the cell
 * and centre-crop the overflow. `contain` would letterbox, which is what a
 * preview that lies looks like — the customer would approve a composition the
 * printer then crops.
 *
 * Deliberately not a server render. Composing the real sheet is a 300 dpi job
 * that takes seconds, and a customer taps between templates; the file the
 * printer gets is composed once, when they commit.
 *
 * The filter is applied to the photos and not to the sheet, which is what
 * compose.Sheet does: it colours the customer's photograph, never the frame the
 * designer drew.
 */
export function SheetPreview({
  template,
  chosen,
  filter,
}: {
  template: Template;
  chosen: Photo[];
  filter?: string;
}) {
  const [sheetW, sheetH] = template.sheet;
  const pct = (n: number, of: number) => `${(n / of) * 100}%`;

  return (
    <div className="sheet" style={{ aspectRatio: `${sheetW} / ${sheetH}` }}>
      {template.background && <img className="sheet-art" src={template.background} alt="" />}

      {template.cells.map((c, i) => {
        const photo = chosen[i];
        return (
          <div
            key={i}
            className="sheet-cell"
            style={{
              left: pct(c.x, sheetW),
              top: pct(c.y, sheetH),
              width: pct(c.w, sheetW),
              height: pct(c.h, sheetH),
            }}
          >
            {photo ? (
              <img src={api.photoURL(photo.id)} alt="" style={{ filter: filterCSS(filter ?? "") }} />
            ) : (
              // The empty cell shows its own number, which is the number the
              // filmstrip stamps on a frame when it is picked. Tapping order is
              // the order the photos print in, so the two have to agree.
              <span>{i + 1}</span>
            )}
          </div>
        );
      })}

      {/*
        Last, and over the photos — draw.Over in compose.Sheet, so the
        designer's transparency means on screen what it means on paper.
      */}
      {template.overlay && <img className="sheet-art" src={template.overlay} alt="" />}
    </div>
  );
}
