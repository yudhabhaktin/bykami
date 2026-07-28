# Kiosk illustration assets

## `selfie.svg`

From [Open Doodles](https://www.opendoodles.com) by Pablo Stanley, **CC0 public
domain** — "Free for Commercial and Personal Use. No need to credit, license, or
anything."

CC0 is why this one could be committed at all. The frame overlays in
`agent/internal/compose/templates/` had to be drawn from scratch instead,
because every stock source found for those carried attribution or share-alike
terms and this repository is public. No attribution is required here; it is
recorded because knowing where a file came from is worth more than the two lines
it costs.

Modified on import, all of it reversible from the original at
`https://opendoodles.s3-us-west-1.amazonaws.com/selfie.svg`:

- Sketch's generator comment, `<title>` and `<desc>` removed.
- `#000000` → `#1a1a1a` (ink) and `#FF5678` → `#be3a31` (deep red), so the
  illustration sits in the booth palette rather than beside it.

Colours are baked into the file rather than driven by `currentColor`, because it
is served as an `<img>` and the two fills are not interchangeable — one is the
line, one is the accent.
