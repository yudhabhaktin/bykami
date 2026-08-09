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

The studio page has a photo grid at `sites/studio/public/img/studio-0*.jpg`, and
**it now holds six real sessions**, pulled from the studio's own Instagram at
1440 × 1800 and downsampled to 900 × 1125. The generated stand-ins are gone. The
grid is 4:5, two columns on a phone and three from 640px; replacing any frame is
dropping a file over the same name, so long as it is 4:5.

Resolution turned out not to be the problem the note at the bottom of this file
predicts — Instagram serves the embed a full-size image, not a thumbnail. What
is still worth improving:

- [ ] **Clean versions of 01, 02, 03 and 06, without the overlaid type.** Four of
      the six carry the studio's own marketing lettering — `PHOTOSHOOT`,
      `LEBARAN`, `NEW LOOK`, `FUNGKY MOODBOARD`. Right for a feed, less right
      for a portfolio grid, where the type competes with the photograph and
      dates it to a campaign. The unlayered exports exist on whatever machine
      made the posts.
- [ ] **The room itself, empty.** Still missing entirely, and still the one a
      first-timer most needs: they are buying a room they have never seen, and
      this page carries a `LocalBusiness` block with the real Jajag address on
      it. Lighting rig, backdrop and remote shutter visible, lights on, nobody in
      frame. Ten minutes and a phone on a chair.
- [ ] **A printed 4-panel strip, held.** The signature format per
      `direction.md`, and the only thing that shows what a customer leaves with.
      There was a good mockup of this; a photograph of a real one is better.
- [ ] **A pas foto example.** Three of the seven packages are ID photographs,
      two of them for marriage registration, and nothing in the grid or the
      gallery shows one. Anyone arriving from a search for *pas foto Banyuwangi*
      currently sees six casual sessions and no evidence the studio does the
      thing they came for.

Requirements for anything new: originals from the camera; 4:5 portrait, at least
1400px on the short edge; nothing important in the outer 5%, because the grid
fills a 4:5 box with `object-fit: cover`; and consent from anyone recognisable.

## The team photographs on bykami.id

Two, both lifted out of `@ddnappn`'s twenty-frame album (`DYj7W_BE2Hx`) with the
owner's go-ahead on 2026-08-09.

| File | Size | What it is |
|---|---|---|
| `tim-01.jpg` | 1354 × 1026 | The team under the "Tabuhan Island Banyuwangi" sign |
| `tim-02.jpg` | 1440 × 600 | Eleven of them holding hands in a line at the water's edge |

**How they were recovered, because it is not obvious from the album.** Every one
of those twenty frames is a scrapbook layout — handwriting, polaroid borders,
torn paper — and as whole frames they are unusable on a page built on restraint.
But several are a clean photograph with the decoration arranged *around* it, and
the photograph lifts back out. These were cropped in a headless browser at exact
pixel bounds rather than by eye. Working crops are kept in `refs/curation/crops/`
alongside the originals; `crop.py` there records the bounds, so a frame can be
recut without finding them again.

This replaced the reel cover that was here first — `DX6WZo8POuM` at 2305 × 4096,
sharper than either of these. Resolution lost the argument: its subject was a
printed holiday banner held up to the camera, which announces that the staff went
on a trip rather than showing who they are.

- [ ] **Consent from the eleven people in them.** Same rule as the gallery, and
      sharper here: this is the platform root and it is the first picture anyone
      sees. Two may be the trip's photographers (`@banyuwangimoment`,
      `@fiqrulrs` are thanked in a companion post) rather than staff. Drop a
      replacement over the same file name if anyone asks.
- [ ] **A team photograph taken on purpose.** Both are holiday snaps doing a job
      they were not shot for — everyone is in swimwear on a beach, which is warm
      and real but is not the studio. A group shot in the room, or at the
      shopfront, would say the same thing about the people and also show where
      they work.
- [ ] **Something that is not the same trip.** Six of the nine links the owner
      supplied are the two "Holiday Edition" outings, so the culture the site can
      currently evidence is one holiday told several ways. Day-to-day frames —
      setting up a booth at an event, a print coming off the DS-RX1HS, the
      counter mid-session — would carry more.

What the album does **not** contain, despite appearances: a set of individual
portraits. Frame 08 is a 3 × 3 grid that looks like one, and the nine cells cut
cleanly, but two hold couples or a family group, one is shot from behind, and
another has marketing type across it. What is left is people on holiday in
sunglasses, not a way to introduce a team.

The other two personal accounts are unused. `@bahtyarsfyn_`'s `DX9Cdg3Oj8a` is a
reel, so it has no still worth having, and `@rama_bareng29`'s `DZC0pZbkubD` is an
ATV album whose one clean frame carries a "50%" watermark.

**On consent specifically.** The six in the grid are recognisable customers.
They are already public on the studio's own account, but a website is a
different surface from a feed and the people in them agreed to the feed. Worth
a message to each. If anyone asks to come down, drop a replacement over the same
file name.

If a frame ever has to be generated rather than shot, the prompts and the craft
rules are in `image-prompts.md`.

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
