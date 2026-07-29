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
      DS-RX1HS actually cuts, not true 2R at 6×9 cm. The existing `strip-3` and
      `strip-4` templates are therefore correct at 600×1800 px, and artwork can
      now be specified against them:

      | Template | Sheet | Cells |
      |---|---|---|
      | `strip-3` | 600 × 1800 | 3 |
      | `strip-4` | 600 × 1800 | 4 |
      | `4r-polos` | 1200 × 1800 | 1, full bleed |

      All at 300 dpi. A frame is a **PNG with the photo areas fully
      transparent** — those holes are what the console reads the cells out of,
      so a hole filled with white is a white box printed over the customer's
      face. The upload is rejected outright at any other canvas size, which is
      what stops artwork being stretched to fit.
- [ ] **Reword the price list.** It sells "cetak strip 2R", which names a size
      the booth does not print. Wrong on a customer-facing price list in a way
      that invites an argument at the counter
- [ ] **Print one strip and measure it.** Bleed, safe area and cut marks are
      still a guess. This matters more now that frames are uploaded rather than
      committed: every design an operator draws inherits the margin convention
      the three house frames set, and correcting it after forty exist is forty
      files to redraw

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

---

**Why not just pull these from Instagram:** IG re-encodes uploads and caps
resolution. Images pulled from the site look acceptable in a grid and visibly
soft as a hero or full-width section. The logo has the same problem — a raster
logo can't be scaled, recoloured, or rendered crisply on a retina display.
Originals solve all of it at once.
