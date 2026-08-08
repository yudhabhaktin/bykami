# Assets needed from the studio

Send this list to whoever owns the studiobykami brand. Everything here is
either impossible or unreliable to recover from Instagram screenshots.

## Blocking — can't finalise design without these

**Brand**
- [ ] Logo — original vector (`.svg` / `.ai` / `.pdf`), not a PNG export
- [ ] Logo variants — light-on-dark, dark-on-light, icon-only
- [ ] Brand colours — actual hex values
- [ ] Fonts — the typeface used in the wordmark and in promo graphics,
      plus confirmation you're licensed for web use (`@font-face` /
      Google Fonts / Adobe Fonts)

**Commercial**
- [ ] Package list — name, duration, max people, prints included, price (IDR)
- [ ] Any add-ons — extra prints, extra time, props, digital files
- [ ] Current promos — the Buy 1 Get 1 and free-photobox mechanics seen on IG
- [ ] WhatsApp number for the booking CTA

**Practical**
- [ ] Full address + Google Maps link
- [ ] Opening hours, including weekend/holiday variation
- [ ] How slots work — walk-in, or booked ahead? Slot length?
- [ ] Backdrop options — list plus one clean photo of each
      (the `Background ...` highlight suggests this is a selling point)

## Blocking — the kiosk

**Hardware, from the studio PC itself**
- [x] Printer — **DNP DS-RX1HS**. 700 × 4×6 per roll, 12.4 s a print, native
      2-inch cut giving two 2×6 strips per sheet. Status and media-remaining are
      queryable on both Windows and Linux
- [ ] Which media is loaded right now, and how many spare rolls are on hand
- [ ] Printer firmware version — media-remaining reporting on the Linux backend
      needs ≥ 2.00
- [x] Booth PC — **Windows 11 Pro**. Chosen for Assigned Access lockdown; DNP's
      driver and SDK are Windows-first
- [x] Camera — **Canon EOS 200D**, 24.2 MP. EOS Utility tethers it, has a
      destination-folder setting, and transfers on the physical shutter release
- [x] Input device — **touchscreen**, and the on-screen tap fires the shutter.
      The handheld remote goes away, which also removes the WiFi/USB conflict
      that would otherwise have broken tethering
- [ ] Touchscreen make/model and how it connects (USB touch + HDMI, or an
      all-in-one), and whether it is reachable from the posing position
- [ ] CPU, RAM and free disk on that PC
- [ ] Whether that PC does anything else today

**Frame artwork**
- [ ] The existing "free desain frame" layouts as **print-resolution files**,
      not exports from a preview — these already ship with booth packages, so
      they exist somewhere
- [ ] Confirmation of bleed, safe area and cut marks, or permission to redraw
      them to a spec
- [x] **"2R" resolved — the product is the 2×6 in strip** (5×15 cm), what the
      DS-RX1HS actually cuts, not true 2R at 6×9 cm. Artwork can now be
      specified against a sheet size:

      | Sheet | Pixels | Note |
      |---|---|---|
      | `4r` | 1200 × 1800 | 4×6. What the house frames use, laid out two-up so the cut yields two strips |
      | `strip2x6` | 600 × 1800 | One strip, which the printer duplicates onto a 4×6 |
      | `6x8` | 1800 × 2400 | |

      All at 300 dpi. A frame is a **PNG with the photo areas fully
      transparent** — those holes are what the console reads the cells out of,
      so a hole filled with white is a white box printed over the customer's
      face. The upload is rejected outright at any other canvas size, which is
      what stops artwork being stretched to fit.
- [ ] **Reword the price list.** It sells "cetak strip 2R", which names a size
      the booth does not print. Wrong on a customer-facing price list in a way
      that invites an argument at the counter
- [ ] **Print one sheet and measure it.** Bleed, safe area and cut marks are
      still a guess. This matters more now that frames are uploaded rather than
      committed: every design an operator draws inherits the margin convention
      the house frames set, and correcting it after forty exist is forty files
      to redraw. It matters more again now that those frames are laid out
      two-up — the cut lands in a gutter nobody has measured, and a blade 2 mm
      off centre shaves one strip and leaves a stripe of the other on it

Importing these beats building an authoring tool, and it sidesteps the logo
vector being blocked. **The importer exists** — upload at `app.bykami.id`, and
the booths pull what is published. What is missing is the artwork, not a place
to put it.

## Needed for build quality

- [ ] 15–25 original customer photos at full resolution, with model release
      or permission to publish — **not** Instagram downloads
- [ ] 3–5 photos of the studio space itself, empty and lit
- [ ] A few original 4-panel strip layouts (the signature format)
- [ ] Confirm copy language — Indonesian only, or ID + EN toggle?

## Nice to have

- [ ] Customer testimonials or Google review screenshots
- [ ] Any existing brand guideline document, however rough
- [ ] Analytics or pixel IDs to embed (Meta pixel, GA4)

## Shot list — the gallery on studio.bykami.id

The studio page has a photo grid, and six frames are in it at
`sites/studio/public/img/studio-0*.jpg`. They match the brief below one for one,
and the grid is built around them: 3:2 landscape, 1200 × 800, two columns from
640px. Replacing any of them is dropping a file over the same name — no code
change, so long as the replacement is 3:2.

**Five of the six are generated, not shot.** That is fine as brand imagery and it
is not fine as evidence, so the frames are not equal and the list below stays
open rather than ticked:

- [x] **06 — a printed 4-panel strip, held.** Real, house layout, real lockup.
      Done. The only frame showing what a customer actually leaves with.
- [ ] **05 — the room itself, empty.** The urgent one. An empty studio is read as
      *this* studio, and this page carries a `LocalBusiness` block with the real
      Jajag address on it, so the picture is a claim about premises a customer
      can walk into and check. Lighting rig, backdrop and remote shutter visible,
      lights on, nobody in frame.
- [ ] **04 — a group of four or more.** Currently four models. It is standing in
      for BIG MAXI's ten-person capacity, which is the one number on the price
      list a photograph could corroborate.
- [ ] **03 — two people.** The most-booked shape, and what "Untuk berdua" on the
      MINI card is describing.
- [ ] **01, 02 — two solo portraits, different backdrops.** Lowest risk of the
      five; nobody reads a model as a named customer. Real ones would still show
      the actual lighting and prove the backdrop is a real choice, which the
      highlights treat as a feature.

Requirements, so they survive the layout: originals from the camera, not
Instagram re-encodes; landscape, at least 2400px on the long edge; nothing
important in the outer 5%, because the grid fills a 3:2 box with
`object-fit: cover`; and written consent from anyone recognisable, since these
become marketing.

Also still needed for the sections that are built but empty:

- [ ] **Real numbers for a trust block** — sessions shot, years open, Google
      rating, review count. Whichever are true. The reference template ships
      "39 projects, 0 awards, 10% success rate" as unfilled placeholders, which
      is the failure mode to avoid: no number is better than a made-up one.
- [ ] **One customer quote with a name and permission**, for a testimonial card.

---

**Why not just pull these from Instagram:** IG re-encodes uploads and caps
resolution. Images pulled from the site look acceptable in a grid and visibly
soft as a hero or full-width section. The logo has the same problem — a raster
logo can't be scaled, recoloured, or rendered crisply on a retina display.
Originals solve all of it at once.
