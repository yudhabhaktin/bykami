# Design direction — reference read

Derived from `refs/screenshots/01-profile-grid.png` (logged-out capture of
[@studiobykami](https://www.instagram.com/studiobykami/), 2026-07-25).

> **Confidence note.** The capture is dimmed by Instagram's signup scrim, so
> *structure, type, and content* below are reliable but **colour values are
> not** — everything reads darker than it is. Palette is marked TBC until a
> clean screenshot replaces it.

## The business

Self-photo studio and photo booth in **Banyuwangi**, East Java.
131 posts · 5,795 followers · 233 following.

Sister accounts: `@boothbykami` (booth), `@photobykami_` (photography).
The bio also lists "Photographer" — so this is studio rental *plus* paid shoots.

Self-photo studios are a Korean-import format: customers rent a lit studio with
a remote shutter and shoot themselves, unsupervised, in a fixed time slot. That
shapes the whole page — the product is **time in a room**, not a photographer.

## Brand marks

- **Logo** — black circle. A thin outlined rounded-rectangle "frame" glyph,
  then `kami` in a light, wide-tracked geometric sans, all lowercase.
  `studio by` sits above in a much smaller script/italic face.
- **Voice** — casual Indonesian, youth-directed. Observed copy:
  *"Like 50K liburan akhir tahun gassskan"*, *"Promo Buy 1 Get 1"*.
  Slangy (`gassskan`), not corporate.
- **Community framing** — highlights are named `#SobatKAMi4`, `Story Kamu`,
  `Story Kamu 2`, `Story Kamu 3`. "Sobat KAMi" = *KAMi's friends*; "Story Kamu"
  = *your story*. Customers are treated as members, and their photos are the
  content. That's the marketing engine and the landing page should carry it.

## Grid content mix

Roughly, from the visible rows:

| Type | Notes |
|---|---|
| Customer group shots | 2–4 friends, studio-lit, heavy black & white |
| 4-panel photobooth strips | Classic 2×2 contact-sheet layout — the signature format |
| Promo graphics | Bold condensed display type, high contrast |
| Voucher / offer cards | Serif display (`FREE PHOTOBOX`), light-on-dark, more premium |
| Brand collabs | Mie Gacoan tie-in visible — local F&B partnerships |

Props are minimal and cheap: hats, sunglasses. Walls are white with framed
prints. Customers include hijab-wearing groups — design must read as
locally inclusive, not imported-Seoul-cool.

## Highlights → product surface

The highlight reel doubles as a feature list, and should map to page sections:

- `Background ...` → **backdrop choice is a selectable feature**
- `Studio Hari Ini` → *studio today* — current setup / rotating theme
- `Kamistage 5.0` → a named event or studio version
- `Story Kamu` ×3 → customer galleries / social proof

## Palette — TBC

Not extractable from a dimmed capture. What *is* structurally clear:

- Monochrome-dominant photography (B&W treatment on most customer shots)
- Near-black brand surface (logo, voucher cards)
- White studio walls as the bright counterweight
- Colour arrives via promo graphics only (magenta/cyan seen in a Gacoan tile)

Needs a clean screenshot before committing hex values.

## Implications for the landing page

Structure this around booking, not portfolio:

1. Hero — what it is, where, and a **Book** CTA above the fold
2. Packages & pricing — duration, headcount, print count
3. Backdrop picker — visual, since it's a highlighted feature
4. Gallery — customer photos, monochrome-leaning grid
5. How it works — self-photo needs explaining to first-timers
6. Promo strip — the Buy 1 Get 1 / voucher mechanic is clearly core
7. Location, hours, WhatsApp contact

The 4-panel strip is the strongest visual asset. It's a native grid unit and
should drive the layout rhythm rather than a generic 3-column card row.

## Decisions taken

- **Whose brand:** studiobykami's own. So this is brand *implementation*, not
  brand creation — real assets come from the owner, not from this reference
  read. See `assets-needed.md`.
- **Stack:** Astro, with React only for interactive islands (backdrop picker,
  gallery lightbox). Supersedes an earlier "React frontend, Go backend" note —
  prices have to be crawlable HTML, and Astro ships no JS by default.
- **Primary CTA:** WhatsApp click-to-chat with a prefilled message.

### Consequence: no backend in phase 1

WhatsApp booking is entirely client-side — a `wa.me` link with an encoded
message. It needs no server. The backend would earn its place only by serving
*editable content*: packages, prices, backdrops, gallery — letting the studio
change a price without a redeploy.

**Settled: it doesn't earn it, not yet.** An editing interface needs
authentication, and authentication is the phase 2 identity system; building a
throwaway admin auth now is waste. Phase 1 is fully static — the catalogue lives
in the repo as schema-validated content collections, so a price change is a
commit. A studio changes prices a few times a year, not daily.

A git-backed CMS commits to those same files, so it can be added later without
changing the architecture.

## Still open

- Pricing, hours, address, WhatsApp number — none legible in the capture
- Palette and typeface — blocked on original brand assets
- Copy language: Indonesian only, or ID + EN?
