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
| `design/infrastructure.md` | VPS memory budget, Cloudflare Tunnel, Terraform/Ansible split, CI/CD |
| `design/assets-needed.md` | Checklist of assets only the owner can supply |

## Phasing

1. **Marketing pages** — static, SEO-first. Booking links point at the existing
   YouCanBook.me calendars. Ships independently.
2. **Identity + loyalty + booking + QRIS** — built together against a shared user
   model. Gated on Xendit merchant onboarding.
3. **F&B vertical** — reuses identity, loyalty, payments, notifications.

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
- [ ] Phase 1 built

## Blocked on the owner

- Bookable resource count at the studio (blocks booking design)
- Capacity conflict: PDFs say 1–4 / 1–6 / 1–10, YouCanBook.me says 3–4 / 5–6 / 7–10
- `bykami.com` registration and DNS access
- Logo vector, brand hex, licensed fonts, original photography
- Business entity, NPWP, bank account — gates Xendit onboarding
