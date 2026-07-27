# Built-in templates

Geometry only. These three exist so that a booth with no artwork installed can
still complete a session end to end — they are not the design work.

The real templates are **flat files that already exist**: booth packages
advertise *"free desain frame"*, so the artwork has been drawn. Importing it is
content work, and `design/kiosk.md` is explicit that importing beats building an
authoring tool, and that 99+ templates versus six is the actual gap against the
incumbent. It also sidesteps the logo-vector blocker in `assets-needed.md`.

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

`design/kiosk.md` lists bleed, safe area and cut marks as undecided. Until they
are settled, the margins here are a guess that looks reasonable rather than a
measurement against a printed sheet — check one on the real printer before
anyone draws forty designs against these numbers.
