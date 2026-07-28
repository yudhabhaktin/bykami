# Frame artwork

The source for the strip overlays in
`agent/internal/compose/templates/{strip-3,strip-4}/overlay.png`. The PNGs are
committed; this directory is how they were made and how to change them.

## Why it is rendered in a browser

An overlay is a print asset — 600×1800 at 300 dpi, real type, curves that have
to stay smooth at 5 cm wide. Drawing that in Go against `x/image` meant blocky
bitmap type, which is what the previous placeholders looked like and why they
were obviously unshippable. Chrome already has a text shaper and a path
rasteriser, so the artwork is written as HTML/CSS/SVG against the same tokens as
the kiosk UI and screenshotted at exact pixel size.

The trade is that regenerating needs a browser and the fonts named in
`frame.html`, which is why the output is committed rather than built in CI.

## Why there are two passes

`compose.Sheet` draws background → photos → overlay, with the overlay composited
`draw.Over`. The cells must therefore be **transparent** in the overlay or the
frame would cover the photos it is supposed to surround.

Chrome cannot screenshot with a transparent background here, so the frame is
rendered twice at identical geometry:

- **art** — the frame as it prints, cells left as ground
- **mask** (`?mask=1`) — white everywhere the frame is opaque, black inside the
  cells

`punch.go` then takes RGB from the art and alpha from the mask's luminance. The
alternative — punching rectangles with known coordinates — would give hard
edges, and the cells have rounded corners: a stair-stepped photo edge against
the ink ring is visible at print resolution.

The hole is inset **4 px inside** the manifest cell so the ink ring laps the
photo's outer edge rather than butting against it. That costs about 0.34 mm of
photo per side and is why the corners look drawn rather than clipped.

## Regenerating

```bash
cd design/frames && python3 -m http.server 8912
```

Then, at a viewport of exactly **600×1800**, screenshot each of:

```
http://127.0.0.1:8912/frame.html?t=4          → s4art.png
http://127.0.0.1:8912/frame.html?t=4&mask=1   → s4mask.png
http://127.0.0.1:8912/frame.html?t=3          → s3art.png
http://127.0.0.1:8912/frame.html?t=3&mask=1   → s3mask.png
```

```bash
go run punch.go s4art.png s4mask.png ../../agent/internal/compose/templates/strip-4/overlay.png
go run punch.go s3art.png s3mask.png ../../agent/internal/compose/templates/strip-3/overlay.png
```

`punch.go` prints the opaque/transparent split. A sane strip is roughly a third
opaque; if it reports near 0% transparent the mask pass did not render, and the
overlay would print as a solid card over the customer's faces.

## The geometry is duplicated, deliberately

`frame.html` restates the cell rectangles from each `template.json`. They must
match: the hole punched here is where the photo shows through. It is duplicated
rather than imported because the renderer is a static page with no build step,
and a build step to remove one small duplication would be the more expensive
mistake. `compose` will not catch a mismatch — it composites whatever it is
given — so **check a composed sheet after any geometry change**, not just the
overlay on its own.

## Fonts

`Avenir Next` for the wordmark and `Bradley Hand` for the handwritten line, both
macOS system faces, with Windows and generic fallbacks named. Rasterising text
with an installed font into an image is ordinary use and does not redistribute
the font — unlike bundling a woff2, which is why `apps/kiosk/src/styles.css` has
the opposite constraint.

Regenerating on a machine without those faces will silently substitute and the
type will shift. If that matters, the fix is to commit an OFL face here and name
it explicitly.

## 4R has no overlay, on purpose

`4r-polos` is *polos* — plain. Its single cell is the entire 1200×1800 sheet, so
any overlay would cover the photograph rather than frame it. Giving 4R a frame
means a **new template** with a smaller cell, not an overlay on this one.
