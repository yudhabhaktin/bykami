# Where the build is

Reviewed 2026-08-05. `README.md` describes what the system is for; this file
records how much of it exists.

Three states are used throughout. **Live** means customers touch it. **Built**
means it runs and is tested, but something stands between it and a customer.
**Blocked** means the next step is not ours to take.

## Phasing

1. **Marketing pages** — static, SEO-first. Booking links point at the existing
   YouCanBook.me calendars. Ships independently.
2. **Identity + loyalty + booking + QRIS** — built together against a shared user
   model. Gated on Xendit merchant onboarding.
3. **Kiosk** — self-service capture, print and file delivery at the Jajag
   studio. Runs in parallel with phase 2 because it is the only workstream not
   blocked on an external party. See `design/kiosk.md`.
4. **F&B vertical** — reuses identity, loyalty, payments, notifications.

Phase 3 overtook phase 2 for exactly the reason it was scheduled in parallel:
nothing in it needed a merchant account, a phone provider or a legal entity.

## What runs today

| Surface | State | Notes |
|---|---|---|
| `bykami.id` and the three vertical sites | Live | Dimsamcong is built but held out of the index until it has a menu |
| `app.bykami.id` — API and operator console | Built | Serves `/healthz` and the booth frame sync. Auth answers 503 |
| The booth — a full paid session end to end | Built | Runs on a laptop and on the test VPS. Payment and capture are simulated; the printer backend is real but has never met the printer |
| `booth-test.bykami.id` | Built | Temporary. One access token per tester, auto-deploying from `agent-<sha>` |
| The VPS | Built | Alibaba ECS trial box, Singapore. Synthetic data only |
| Booking, QRIS, WhatsApp delivery | Blocked | See the owner list below |

## Phase 1 — marketing pages

- [x] Platform architecture decided
- [x] Catalogue verified — ~25 priced items across six service lines
- [x] Four sites built, deployed and live on Cloudflare Pages
- [x] SEO contract asserted in CI against built HTML, not components
- [~] Design direction — structure, type and content done. The palette is
      blocked on original brand assets, so the sites ship on a placeholder

## Phase 2 — identity, loyalty, booking

- [x] Phone-first accounts, OTP challenges and sessions in `api/internal/identity`
- [x] The append-only `#SobatKAMi` loyalty ledger, with concurrency tests that
      prove a retried webhook credits once
- [x] Operator console — server-rendered, no JavaScript, `__Host-` cookie, CSRF
      derived from the session token
- [x] Deployed to the VPS behind Cloudflare Tunnel, with a health-gated rollout
      and nightly backups copied off-box to R2 under a storage cap
- [~] **Auth is closed on the deployed box, deliberately.** With no OTP delivery
      configured every auth route answers 503, which also means nobody can sign
      in to the console. Two gates hold it: data residency is unresolved, and
      the only sender that exists writes codes to a log
- [ ] Earn and burn have no HTTP route. That needs a device credential which is
      neither a customer session nor an operator one — the open question in
      `design/kiosk.md`
- [ ] Booking, blocked on the bookable-resource count

## Phase 3 — the booth

- [x] A full session: pay, choose a frame, a self-running photo session on a
      countdown, choose photos and a filter, print, collect by QR
- [x] Composition at 300 dpi from the originals, with the filter applied to the
      pixels that print rather than to a CSS preview
- [x] Print queue and an append-only media ledger, counting sheets rather than
      copies
- [x] Consent capture, and a 7-day purge of originals, sheets, derivatives and
      clips that does not depend on anyone remembering
- [x] A download page the booth serves itself — stills, the composed sheet as
      printed, and five seconds of animated GIF behind every frame
- [x] Frame catalogue in `api/`: upload a PNG, cells are read out of its own
      transparent regions, publish it and every booth pulls it within five
      minutes. Seven built-in Gacoan collab designs ship in the binary as the
      fallback
- [~] **Payment and capture run against simulations.** Each is an opt-in flag
      with a startup warning, standing in for hardware or an account that does
      not exist yet
- [~] **The DNP prints through the Windows spooler**, in pure Go against
      `gdi32` and `winspool` — no SDK, no cgo, and the release still
      cross-compiles. It picks between two print queues for the customer's cut
      choice, refuses a job the queue's page size does not match, and cancels
      the spool job on any path that gives up so a failed print cannot reappear.
      Never run against an RX1HS; `agent/README.md` lists the three things only
      the printer can settle
- [ ] No shutter release. `-source=hotfolder` announces the countdown and a
      person fires the camera. The recommended path is a USB relay into the
      RS-60E3 jack
- [ ] No WhatsApp sender, no liveness heartbeat, and no per-booth identity —
      one shared secret admits every booth

`agent/README.md` → *Not here yet* and `api/README.md` → *Not here yet* carry
the full list with the reasoning for each.

## Phase 4 — F&B

Not started. The `dimsamcong` property is built and deliberately unindexed,
because there is no menu to build a catalogue from.

## Infrastructure

- [x] Cloudflare zone, four Pages projects, Zero Trust tunnel and DNS in
      Terraform, state in R2
- [x] The VPS bootstrapped by Ansible: swap, SSH hardening, unattended upgrades,
      `cloudflared`, the app, backups
- [x] CI reaches into no machine. Both boxes poll GitHub Releases and install
      their own updates, with checksum verification and rollback
- [~] The box is an **Alibaba ECS trial instance in Singapore**, not
      Terraform-managed, and holds **synthetic data only until residency is
      settled**. `design/infrastructure.md` records the R2-versus-OSS fork
- [ ] Releases are not signed. Anyone who can push to `main` can publish a
      release both boxes will install — the same trust boundary as any CI
      deploy, and the reason the pollers are opt-in

## Blocked on the owner

Nothing here is a code problem. Roughly in order of what it unblocks.

| Waiting on | Unblocks |
|---|---|
| Confirmation of the unverified prices | Turns `Offer` schema on across the sites. 66 facts currently render without structured data |
| A WhatsApp provider account | OTP delivery, and with it every auth route and the operator console |
| Business entity, NPWP, bank account | Xendit onboarding, and with it the booth taking real money |
| Bookable resource count at the studio | Booking design |
| Resolution of the capacity conflict — PDFs say 1–4 / 1–6 / 1–10, YouCanBook.me says 3–4 / 5–6 / 7–10 | Correct headcounts on the studio pages |
| Dimsamcong menu | Indexing the F&B property |
| Logo vector, brand hex, licensed fonts, original photography | The real palette, and hero imagery at full resolution |
| The existing "free desain frame" layouts as print-resolution files | Frames already advertised on booth packages. The Gacoan collab set covers the booth meanwhile |
| Which printer media is loaded, and spare rolls on hand | Accurate media accounting. The hardware itself is known: DNP DS-RX1HS, Windows 11 Pro, Canon EOS 200D, touchscreen |

`design/assets-needed.md` is the full checklist to send the owner; `pnpm
coverage` prints every fact still waiting on one of these, and CI posts it to
every run's summary.
