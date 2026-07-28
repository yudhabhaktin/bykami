# Built-in templates

These three exist so that a booth with no artwork installed can still complete a
session end to end.

## The two strip overlays

`strip-3/overlay.png` and `strip-4/overlay.png` are drawn to the house design
language — cream mat, ink outlines, the wordmark and a handwritten line, a
botanical mark at each foot. Source and the regeneration steps are in
`design/frames/`; edit `frame.html` there rather than the PNGs.

They are ours rather than downloaded because every stock source carries
attribution or share-alike terms and this repository is public — the same reason
`refs/screenshots/` is gitignored. Drawing them makes them ours to publish, and
that constraint is why a competitor's frames are not an option however
convenient they look.

`4r-polos` has no overlay and must not get one: *polos* means plain, and its
single cell is the whole sheet, so any overlay would cover the photograph.
Giving 4R a frame means a **new template** with a smaller cell.

These are the house frames, not the catalogue. `design/kiosk.md` is explicit
that 99+ templates versus six is the actual gap against the incumbent, that
importing beats building an authoring tool, and that the studio's existing
*"free desain frame"* files are flat artwork that already exists. Importing
those is still content work and still worth doing.

## Adding one

A directory with a `template.json`, plus any PNG or JPEG it references:

```json
{
  "name": "Wisuda 2026",
  "layout": "strip2x6",
  "background": "bg.jpg",
  "overlay": "frame.png",
  "cells": [
    { "x": 30, "y": 36, "w": 540, "h": 450 }
  ]
}
```

| Field | |
|---|---|
| `layout` | `4r`, `strip2x6` or `6x8`. Decides the sheet size and the media cost |
| `cells` | Where the photos go, in pixels at 300 dpi from the sheet's top-left. One cell per photo the customer picks |
| `background` | Drawn under the photos. Optional |
| `overlay` | Drawn over them, with transparency. This is the frame artwork, the logo and any footer text. Optional |

Sheet sizes at 300 dpi: `4r` is 1200 × 1800, `strip2x6` is 600 × 1800, `6x8` is
1800 × 2400. A cell that falls outside the sheet is rejected when the template
loads, rather than printed half-off the paper.

Photos **fill** their cell and are centre-cropped. A letterboxed photo inside a
designed frame reads as a mistake, and the customer framed the shot expecting
the whole cell.

Templates in this directory are compiled into the binary — one artifact, one
version, the same rule the embedded kiosk UI follows. An outlet can add its own
without a rebuild by pointing `-templates` at a folder on the booth PC.

## Frame spec still open

The strip size is settled — **2×6 in**, which is what the DS-RX1HS cuts, so
600×1800 at 300 dpi is right (`design/assets-needed.md`).

Bleed, safe area and cut marks are not. The margins here are a guess that looks
reasonable rather than a measurement against a printed sheet, and the ink ring
around each cell is exactly the kind of detail a few millimetres of cut drift
would ruin. **Print one and measure it** before anyone draws forty designs
against these numbers.
