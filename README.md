# bykami

Multi-vertical local platform for Banyuwangi, East Java.

Currently operating: **studio by KAMI** (self photo studio, photobox, pas foto),
**photo by KAMI** (on-location photography and video), and **Dimsamcong** (F&B).
Further verticals planned.

> Directory is still named `studio-landing/`. Rename to `bykami/` when the build
> starts — the scope outgrew the original name.

## Design docs

Read in this order:

| Doc | What it covers |
|---|---|
| `design/platform-architecture.md` | Vertical map, subdomain strategy, shared identity + loyalty, SEO approach |
| `design/direction.md` | Brand and content read for the studio vertical; verified catalogue and NAP |
| `design/booking-phase2.md` | Self-hosted booking to replace YouCanBook.me, QRIS via Xendit |
| `design/kiosk.md` | Self-service capture, print and delivery in the studio; the franchise path |
| `design/infrastructure.md` | VPS memory budget, Cloudflare Tunnel, Terraform/Ansible split, CI/CD |
| `api/README.md` | Phase 2 monolith — the HTTP surface at `app.bykami.id` and why auth is closed |
| `design/assets-needed.md` | Checklist of assets only the owner can supply |

## Phasing

1. **Marketing pages** — static, SEO-first. Booking links point at the existing
   YouCanBook.me calendars. Ships independently.
2. **Identity + loyalty + booking + QRIS** — built together against a shared user
   model. Gated on Xendit merchant onboarding.
3. **Kiosk** — self-service capture, print and file delivery at the Jajag
   studio. Runs in parallel with phase 2 because it is the only workstream not
   blocked on an external party. See `design/kiosk.md`.
4. **F&B vertical** — reuses identity, loyalty, payments, notifications.

Long-term the business is a **franchise** — outlets running `booth by KAMI`
under the existing brand and one pooled loyalty ledger — not photobooth software
sold to other operators. That choice decides the data model; `design/kiosk.md`
records why.

## Reference material

`refs/` holds Instagram captures and the studio's price-list PDFs. All gitignored.

These are **inputs to a design process, not assets**:

- The images are the owner's copyright and this repo is public
- The PDFs are 17 MB of binaries whose value is the prices, which belong in the
  pages as crawlable HTML

Anything shipped must be original, licensed, or the owner's own photography at
full resolution — Instagram downloads are re-compressed and unusable at hero size.

## Status

- [x] Repo scaffolded
- [x] Catalogue verified — ~25 priced items across six service lines
- [x] Platform architecture decided
- [~] Design direction — structure, type, and content done; palette blocked on
      original brand assets
- [x] Phase 1 built and live on `bykami.id`
- [~] Phase 2 — identity, loyalty ledger and SQLite store built in `api/` and
      served over HTTP at `app.bykami.id`; auth routes closed until OTP
      delivery and residency are settled, booking blocked
- [x] Kiosk architecture decided — not yet built
- [~] VPS — Alibaba ECS trial box running in Singapore; not hardened, not
      Terraform-managed, **synthetic data only until residency is settled**
- [~] Phase 2 — identity and the loyalty ledger built and tested; booking and
      QRIS blocked on the two items below

## Blocked on the owner

- Bookable resource count at the studio (blocks booking design)
- Capacity conflict: PDFs say 1–4 / 1–6 / 1–10, YouCanBook.me says 3–4 / 5–6 / 7–10
- Confirmation of the 67 unverified prices — the switch that turns `Offer`
  schema on. Highest-value unblock on the list.
- Dimsamcong menu — the property is built but held out of the index until it has
  one, because there is no source material to build a catalogue from
- Logo vector, brand hex, licensed fonts, original photography
- Business entity, NPWP, bank account — gates Xendit onboarding
- Booth PC, printer and camera specs — gates the kiosk print and capture paths
- Frame artwork as print-resolution files — the "free desain frame" already
  advertised on booth packages, wherever it currently lives
