# Built-in templates

The seven frames of the **Gacoan × studio by KAMI** set. They exist so that a
booth with no artwork installed can still complete a session end to end, and
they are what the booth offers until the catalogue at `app.bykami.id` gives it
something else.

## They are 4R sheets designed to be cut

Every one is `4r` — 1200 × 1800 at 300 dpi, a single 4×6 sheet — laid out two-up
with a gutter down the middle, so the printer's 2-inch cut turns one sheet into
two 2×6 strips. That is the choice the customer makes on the session screen, and
it is a property of the paper: the same pixels are composed either way and only
the blade differs.

The two halves are **not** duplicates. Each holds its own photographs, so a cut
sheet is two strips with different pictures rather than the same strip twice.
Six cells on every one of them, three to a strip.

`strip2x6` is therefore unused by anything shipped. It remains a layout the
printer and the catalogue both understand — an uploaded 600×1800 frame still
works, and the printer still knows a strip yields two copies from one sheet.

## The cells were read out of the artwork, not typed

Each `template.json` restates what `api/internal/frames`.`Detect` reads back out
of its own `frame.png` — the bounding boxes of the transparent regions, to the
pixel. `TestDetectRecoversTheHouseFrames` in that package holds the two to each
other, so re-exporting a PNG without regenerating its manifest fails there
rather than on paper.

That is also why the artwork is named `frame.png` rather than `overlay.png`: it
is the same shape on disk as a frame synced down from the catalogue, which
`agent/internal/framesync` writes with exactly this manifest.

The supplied artwork was 1800 × 2700 — 4×6 at 450 dpi — and was resampled to the
300 dpi sheet before being committed. Cells detected at 450 dpi would be 1.5×
the sheet they have to sit on.

`gacoan-5-langit` arrived as two full-bleed panels and its grid was punched
here, so it holds six photos like the rest of the set. The bands are filled with
the sky read out of the frame either side of them, and the ornaments that
overhang the panels were left alone. Re-exporting that one from the delivered
file brings the two panels back.

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
the whole cell. A cell squarer than the camera therefore crops hard:
`gacoan-5-langit` is 402 × 402, so a 3:2 frame loses a third of its width.

A chosen filter is applied to each cell after the photo is drawn into it, so it
colours the photograph and never this artwork. See `filter.go`.

Templates in this directory are compiled into the binary — one artifact, one
version, the same rule the embedded kiosk UI follows. An outlet can add its own
without a rebuild by pointing `-templates` at a folder on the booth PC.

The three sources are **added together**, not chosen between: `cmd/bykami-agent`
appends `-templates` and the synced catalogue onto these, and a frame only
displaces one of them by carrying the same id. So changing this directory never
removes a design from a booth — replacing what a customer is offered means
editing here *and* running `bykami frames unpublish` against the catalogue. The
failure is quiet, because a booth showing too many frames still works.

## These are one outlet's frames

They carry a partner's marks, which the house frames they replaced deliberately
did not: the previous three were drawn in-house precisely so that a public
repository was publishing nothing it did not own. Anything added here inherits
that exposure. A second outlet wanting its own designs should get them through
the catalogue rather than through this directory.

## Frame spec still open

The strip size is settled — **2×6 in**, which is what the DS-RX1HS cuts, so a
4×6 sheet cut in two is right (`design/assets-needed.md`).

Bleed, safe area and cut marks are not. Nothing here has been measured against a
printed sheet, and the gutter each of these designs leaves down the middle is
exactly the kind of detail a few millimetres of cut drift would ruin — a blade
landing 2 mm off centre takes ink off one strip and leaves a stripe of the other
attached. **Print one and measure it** before drawing more against these
margins.
