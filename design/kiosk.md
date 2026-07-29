# Kiosk — self-service capture, print, and delivery

Decision record. The kiosk is the studio's fourth surface, after the three
marketing sites: software the customer touches in the room, replacing the staff
work that currently happens after a session ends.

Built as an internal tool for `studio by KAMI` in Jajag. Designed so that
franchise outlets can inherit it unchanged. **Not** built as multi-tenant SaaS —
see "Franchise, not product" below.

## What this replaces

An earlier plan proposed an Electron kiosk against Postgres, Redis, MinIO,
Docker Compose and Kubernetes, offline-first as the central premise, AI filters
as the differentiator, and multi-tenant SaaS with subscription tiers from day
one. Every one of those is dropped, and the reasons are recorded below rather
than left implicit, because each will look tempting again in six months.

## The competitive read that shaped this

Two facts, verified 2026-07-26, that the original framing had backwards:

- **Sukhakala is not a platform.** Founded 2023, 40+ branches across Jakarta and
  Bandung inside a year. It sells sessions to consumers. You cannot compete with
  it by building software; you compete with it by opening outlets.
- **The software incumbent is [Captura](https://captura.id/).** Sold to
  Indonesian and Malaysian operators at Rp 500k / 750k / 1.2m per booth per
  month, covering DSLR capture, QRIS, DNP printer monitoring, hundreds of
  templates, multi-booth dashboards and remote device control.

So feature parity is not a strategy — Captura is years ahead on features and
sells them cheaply. What Captura cannot sell is a brand with an existing
`#SobatKAMi` community, and that is what a franchise is built on. The software
exists to make outlets cheap to run, not to be the product.

## The success criterion

**Staff-minutes per session, measured before and after.** Not offline uptime.

The studio's real cost is post-session labour: staff pick the shots, print
them, and deliver "all file" by a method `studio.ts` still records as unknown. A
kiosk that lets the customer choose their own frames, print, and leave with a QR
removes most of that. Measure the baseline before writing any code, or there is
nothing to compare against.

Offline capability is a *byproduct* of the local-first architecture below, not a
goal. Booth #1 sits at a fixed address on broadband with the owner on site.

## Architecture

```
        studio PC (Jajag)                        cloud
 ┌────────────────────────────┐
 │  camera → vendor tether    │
 │            ↓               │
 │      hot folder            │
 │            ↓               │        ┌──────────────────────┐
 │   agent (Go, one binary)   │───────▶│ ECS (trial: SG)      │
 │   • watches folder         │  /v1   │ api/ — Go monolith   │
 │   • local SQLite           │        │ SQLite (metadata)    │
 │   • print queue + status   │        └──────────┬───────────┘
 │   • embeds the kiosk UI    │                   │ signed URLs
 │            ↓               │                   ▼
 │  Chrome --kiosk            │        ┌──────────────────────┐
 │  http://localhost:PORT     │───────▶│ R2 (photo objects)   │
 └────────────────────────────┘        └──────────┬───────────┘
                                                  │
                                       gallery.bykami.id
                                       static HTML, no JS
```

Three deployables: `api/` on the VPS, `agent/` on the booth PC, and the gallery
renderer. The kiosk UI is not a fourth — it ships inside the agent binary.

### No Electron

Electron's value here was a local process with hardware access. A Go binary is
that, in a language already used for `api/`, and it **cross-compiles from macOS
with `GOOS=windows`** — where Electron for Windows cannot be built or run on
this machine at all and would need a `windows-latest` runner for every UI
change.

### The agent embeds the kiosk UI

`embed.FS`, one artifact, one version. Version skew between UI and agent is
impossible by construction rather than by handshake.

Two consequences:

- **No service worker.** It existed to survive a missing server; the server is a
  local process that is always present. No precache, no `storage.persist()`, no
  quota eviction, no cold-start-at-a-venue failure mode.
- **UI changes are agent releases.** Fine at one booth, and at N outlets binary
  OTA is needed regardless — so one update mechanism gets built, not two.

### The couplings that remain real

| Coupling | Mechanism |
|---|---|
| agent → `api/` | Versioned path `/v1/…`, never broken; add `/v2` instead. Agent sends its version in a header on every call so the API can log it and refuse below a floor |
| agent → its SQLite | Forward-only embedded migrations at startup — the pattern `internal/store` already uses |

Outlet #1 will be running a six-month-old binary by the time outlet #3 exists.
The version floor is the lever for the day something genuinely must break.

## Capture — hot folder, not SDK

**Decided: the camera's vendor software tethers; the agent watches a folder.**

Canon EOS Utility writes full-resolution JPEGs into a watched directory; the
agent ingests them. The agent never talks to the camera to *receive* a photo —
only, separately, to fire it. Those two concerns stay decoupled, which is what
keeps a vendor SDK out of the build.

### The hardware

**Canon EOS 200D** (Rebel SL2 / Kiss X9), 24.2 MP APS-C. Against a 4×6 print
target of 2.16 MP that is an order of magnitude of headroom — resolution stops
being a design consideration entirely.

EOS Utility does this natively, with no glue:

- It has a **destination folder** preference. That folder *is* the hot folder.
- It transfers on the **physical shutter release**, not only on its own remote
  button — the camera body's shutter works "as you would do normally" and the
  image lands on the PC. The customer's existing habit is unchanged.

### The trigger is the touchscreen

**Decided: the customer taps the screen; the handheld remote goes away.** Input
device is a touchscreen, and the tap starts a countdown that fires the shutter.

This resolves the WiFi/USB conflict outright. Canon disables WiFi whenever USB is
connected, so any shutter path depending on the Camera Connect phone app would
have broken the moment we tethered. Nothing now depends on the camera's radios.

It also reverses two concessions made when the shutter was customer-held:

- **The countdown auto-fires.** A real photobooth 3–2–1 rather than an advisory
  "get ready". This is what customers expect from the format.
- **`maksimal 15x take` becomes enforceable at capture**, not merely counted.
  The app owns the shutter, so it can simply stop firing. The delivery-side
  limit stays as the backstop for what was actually bought.

The tap must be reachable from the posing position, or the countdown has to be
long enough to walk back into frame. Five seconds is the usual answer.

### How the tap reaches the shutter — open

The hot-folder design deliberately avoided shutter control. Getting it back
without also getting cgo and a vendor SDK is the question:

| Path | Cost |
|---|---|
| **USB relay into the RS-60E3 jack** | A few dollars of hardware. The 2.5 mm terminal is tip = shutter, ring = focus, sleeve = ground; the agent opens a serial port and pulses it. ~50 lines of Go, no cgo, works with any future body that has a remote port, and you can hear it click when debugging |
| **digiCamControl** | Free Windows app with an HTTP API that can both trigger and tether, potentially replacing EOS Utility entirely — one moving part instead of two. But it wraps the same EDSDK underneath, and unattended long-run reliability is unproven here |
| **Canon EDSDK via cgo** | Full control, live view, settings. Weeks of work, per-vendor, behind a developer registration. Rejected once already |

Recommended: the relay. It keeps the capture path exactly as designed — EOS
Utility still owns tethering, the agent still just watches a folder — and adds
one serial write. The failure mode is a physical part coming unplugged, which is
visible and cheap to fix, rather than a driver integration that breaks on a
vendor update.

This removes the single largest engineering risk from the build. The
alternative — cgo-wrapping Canon EDSDK or the Sony Camera Remote SDK, per
vendor, on Windows, behind a developer registration — is weeks of work for
shutter control the customer already has in their hand.

**It also protects print quality, which is not negotiable.** Today's prints come
off a real camera at full resolution. Studio output is 4R:

| Output | 300 dpi target | From 1080p webcam |
|---|---|---|
| 4×6, one image | 1200 × 1800 = 2.16 MP | 2:3 crop → 720 × 1080 ≈ **180 dpi, visibly soft** |
| 2×6 strip, whole | 600 × 1800 = 1.08 MP | — composed, not captured |
| One cell in a strip | ~600 × 540 = 0.32 MP | comfortable |

A strip is three or four stacked cells plus a footer, so no single capture ever
fills it. That is why the booth format tolerates low resolution and the studio's
single-image 4×6 does not.

A webcam kiosk would make the one thing customers pay for worse than what the
studio delivers today. Captura sells webcam capture as *Basic* and DSLR as
*Pro* for the same reason.

The cost of this choice: the countdown becomes advisory ("get ready") rather
than auto-firing, and take limits become uncountable at capture. Both are
handled below.

### Session segmentation

The kiosk owns an explicit lifecycle; the folder is a stream the agent
attributes to it.

```
[Mulai] → session OPEN → customer fires shutter → [Selesai] → CLOSED
              ↑                                        ↓
     agent attributes new files          grace window ≈20s still attaches
```

Five details decide whether this works in practice:

- **Debounce writes.** Vendor software writes progressively and `fsnotify`
  CREATE fires on the first byte, so a naive read gets a truncated JPEG. Wait
  until size is stable for ~500 ms **and** the last two bytes are the JPEG EOI
  marker `FF D9`. This is the classic hot-folder bug; it is not hypothetical.
- **Move, don't copy.** On ingest, atomically rename into
  `sessions/<session_id>/`. Same filesystem, so it is a metadata operation. The
  hot folder stays empty, attribution is unambiguous, and the 7-day purge
  becomes a directory delete rather than a query.
- **Attribute on filesystem mtime, not EXIF.** Camera clocks drift and nobody
  resets them after a battery change. Keep EXIF as metadata only.
- **Three buckets.** *Stragglers* land inside the grace window and still attach.
  *Orphans* — staff test shots, accidental fires — go to `unassigned`, visible
  in admin, purged on the same 7-day rule. *Crash recovery* rescans at startup
  for files absent from SQLite; the `photos` table is keyed on content hash so a
  rescan cannot double-insert.
- **Enforce at capture and at delivery.** Now that the app owns the shutter,
  `maksimal 15x take` is enforceable directly — the kiosk shows a live `12 / 15`
  and stops firing at the limit. The selection step still enforces what was
  actually bought ("1 print 4R, 1 file edit") as the backstop, because a stray
  file in the folder must never become a free print.

## Print — DNP DS-RX1HS

The agent owns the print queue, because a browser cannot. `window.print()` is
fire-and-forget by design: no job status, no error, no media remaining. Running
out of media mid-session with no signal is the failure that loses a customer.

Verified specifications, 2026-07-26:

| | |
|---|---|
| 4×6 | 12.4 s — 290 prints/hour |
| 6×8 | 22 s |
| Media | **700 × 4×6 sheets per roll** |
| Sizes | 2×6, 4×6, 6×8 |
| Cut | Native 2-inch cut — **two 2×6 strips from one 4×6**, four from a 6×8 |

### Media is a real constraint on Unlimited Print

One roll yields 700 sheets, or **1,400 strips** at two per sheet. Against the
booth catalogue:

| Package | Sheets consumed | Roll used |
|---|---|---|
| Limited Print — 200 strip | 100 | 14% |
| Limited Print — 400 strip | 200 | 29% |
| **Unlimited Print — 3 or 4 jam** | **unbounded** | see below |

At 12.4 s a sheet, continuous printing exhausts a 700-sheet roll in **~2.4
hours**. So a 4-hour Unlimited Print booking can run a roll dry before the event
ends, and even at half duty cycle it consumes most of one.

**Operational rule the price list does not state: any Unlimited Print booking of
3 hours or more ships with a second roll.** That is a packing-list item, not a
software feature — but the agent's media counter is what makes it actionable
mid-event rather than discovered at the moment it stops.

### Driver path — Windows 11 Pro

Media remaining is queryable, which is the whole reason the agent exists rather
than `window.print()`. **Decided: Windows 11 Pro**, using DNP's own driver and
SDK — the path every photobooth operator runs and the one EOS Utility is best
tested against.

Windows won on **lockdown**, not on the printer. macOS has no equivalent of
**Assigned Access**, and this machine sits alone in a room with customers who
will try Cmd-Tab. Two secondary reasons: DNP's SDK is Windows-first, and Apple
has announced deprecation of legacy CUPS printer drivers in favour of IPP, which
a dye-sub roll printer will never speak.

**Pro specifically, not Home** — Home cannot defer updates or configure Assigned
Access properly. The one real cost of this choice is update reboots; mitigate
with active hours and deferred feature updates.

Linux was viable and was rejected only on lockdown. For the record, in case a
future outlet needs it: the `dnpds40` backend in Solomon Peachy's `selphy_print`
/ Gutenprint supports the RX1HS (as Citizen CY / CY-02) and on firmware ≥ 2.00
reports media offset, iSerial and multi-lot media. An earlier note in this repo
calling Linux dye-sub "patchy" was wrong for this printer.

Layout, page sizing and cut mode remain **per-machine driver configuration**,
not application config.

### Throughput is a non-issue at the studio

1–3 × 4R per session is at most 37 seconds of printing inside a 15–25 minute
booking. The queue matters only for the booth vertical, where it is the
franchise's problem later. Build the counter now regardless — outlets inherit it.

## Delivery — `gallery.bykami.id`

The QR download is the only part of the MVP that genuinely requires a server: a
customer's phone on mobile data cannot reach `localhost`.

It also closes an existing gap. `studio.ts` currently records *"Berapa lama file
saya dikirim?"* → **blocked**, and "delivery method and window unknown".

**Photo objects live in R2, never on the VPS disk.** Serving galleries is the
product's dominant egress, every VPS provider meters it, and R2 does not — at
~$0.015/GB-month with no egress fee. The VPS's SQLite holds session metadata
only, which is what keeps `infrastructure.md`'s "SQLite, not Postgres" intact.

### Compress the delivered files

**Decided: print from the original, deliver a derivative.** The 200D produces
24 MP JPEGs at roughly 6–10 MB each, so a 30-frame session is 180–300 MB of
originals. Sending that to a customer's phone over Indonesian mobile data is
hostile, and storing it is wasteful.

| | Original | Delivered |
|---|---|---|
| Use | Print, local only | Gallery download |
| Size | 6000 × 4000, 6–10 MB | Long edge 2048, JPEG q≈85, ~600 KB |
| Session total | 180–300 MB | ~18 MB |
| Lifetime | Purged locally at 7 days | R2, 30 days |

**Never print from the derivative.** Print quality was the reason for full-res
capture in the first place; recompressing before the printer would give back
exactly what the hot-folder decision bought.

Compression happens **in the agent, before upload** — it saves the studio's
upstream bandwidth, R2 storage, and egress in one step. Pure Go (`image/jpeg`
plus `golang.org/x/image/draw` with CatmullRom) is fast enough: a few hundred
milliseconds per frame, run in the background during a 15–25 minute session, and
it keeps the cross-compile story free of cgo.

**Strip EXIF** on the derivative. Smaller, and it drops the camera's serial
number from every file a customer shares publicly.

The storage arithmetic is why this matters: at ten sessions a day and 30-day
retention, ~18 MB a session is about **5.4 GB rolling — inside R2's 10 GB free
tier.** Uncompressed it would be roughly 70 GB and a monthly bill. Compression is
the difference between free and not.

**Open:** does "all file t&c" promise originals? If customers expect full
resolution, offer originals as a separate opt-in download rather than the
default, so the common path stays cheap.

### The subdomain is a deliberate trade

`gallery.bykami.id` sits inside the `.bykami.id` cookie jar that
`platform-architecture.md` created on purpose for cross-vertical SSO. A separate
registrable domain would have given origin isolation for free; the subdomain
gives free DNS in the existing Terraform-managed zone and keeps the brand.

**Taken, with the gallery held to a surface where cookie theft cannot happen:**

- **No JavaScript.** Static HTML, signed R2 URLs for images.
- **Strict CSP** — `default-src 'none'; img-src <r2-host>; style-src 'self'`. No
  `unsafe-inline`.
- `X-Content-Type-Options: nosniff`, `Cross-Origin-Resource-Policy`, `noindex`,
  and exclusion from the sitemap chain.
- **A contract test guards it**, alongside `tests/seo-contract.test.ts` —
  asserting zero inline script and a present CSP header. That is what stops
  someone adding a lightbox in eight months and quietly reopening the hole.

The unguessable URL *is* the access control. Customers will paste links into
WhatsApp groups, which is wanted — it is free marketing — so expiry is the only
real control. Hence retention below.

## Identity — number for files, verify later

The kiosk runs at `localhost`, a different origin from `bykami.id`. It can never
read the platform session cookie. Kiosk identity is token-based or absent.

**Decided:**

- The **print is unconditional** — they paid, they get it.
- **Digital files require a phone number**, delivered immediately via on-screen
  QR. No OTP.
- The number is stored **unverified**. Loyalty credits only once it is verified
  through the existing `internal/identity` OTP flow, whenever a provider exists.

This captures the number at the moment of peak delight — the customer has just
received photos they like and wants the files — without putting WhatsApp/SMS
provider onboarding on the MVP's critical path next to Xendit. `Sender` has no
implementation today, and that is precisely why OTP is deferred rather than
required.

Unverified numbers never earn, so the append-only ledger stays clean. Its own
rule already says the number *is* the account, and two spellings are two
balances.

### Consent

Two purposes, therefore two consents. Bundling them is the most common PDP
mistake.

> **Ambil file fotomu**
> Masukkan nomor WhatsApp untuk menerima link download.
>
> ☐ Saya setuju foto dan nomor saya diproses untuk mengirim file ini. *(wajib)*
> ☐ Boleh kirimi aku info promo KAMi. Bisa berhenti kapan saja. *(opsional)*
>
> • Foto tersimpan **30 hari**, setelah itu terhapus otomatis.
> • Siapa pun yang punya link bisa membuka galeri — bagikan seperlunya.
> • Di bawah 18 tahun? Minta pendampingan orang tua atau wali.
>
> [Kebijakan Privasi] · **[Kirim link]**

Two requirements that matter more than the wording:

- **Version the consent text**, and store `consent_version` and `consented_at`
  on the session. "They agreed to something at some point" is not a record.
- **Neither box is pre-ticked.** A pre-ticked box is not consent.

## Retention

| Where | Rule | Mechanism |
|---|---|---|
| R2 gallery | Hard delete at **30 days** | Lifecycle rule |
| Booth PC originals | Purge **7 days** after successful upload | Agent, per-session directory delete |

Neither depends on anyone remembering.

**The local rule is the one that matters most, and the original plan had no
equivalent.** A hot folder never empties itself. Twelve months in, an unmanaged
studio PC holds every customer's face at full resolution, unencrypted, on the
machine most likely to be stolen, resold, or handed to a repair shop — sitting
in a room where strangers are left alone with it. Seven days means a theft leaks
a week, not a year.

Retention here is not a cost decision. R2 makes a year of galleries
rounding-error money. It is purely risk versus complaints.

### PDP

UU 27/2022, fully in force since October 2024, classes **children's data and
biometric data** as *data pribadi spesifik*, carrying explicit-consent and
safeguard obligations that ordinary personal data does not. The booth catalogue
serves *acara sekolah*, so some faces are minors'.

Age cannot be verified at a kiosk. The guardian line, short retention, and using
the data only for delivery are **mitigation, not compliance certainty**. This and
the biometric question need someone who practises Indonesian law.

**Gate: no live customer session until residency is settled.** The VPS is
currently an Alibaba free-trial box in Singapore, which is a placement rather
than a decision, and R2 cannot be pinned to Indonesia at all — so the photos, the
sensitive data here, are offshore by default. Until that fork is taken
deliberately, the box carries synthetic sessions only. See
`infrastructure.md` → "Residency is an object-storage problem".

## Franchise, not product

Step two is **franchise**: outlets run `booth by KAMI` under the existing brand
and loyalty program. Not software sold to independent operators.

**Consequence: add `outlet_id` to sessions and loyalty entries now, and keep the
ledger pooled.** One migration, taken before there is data to migrate.

This is a feature rather than a compromise. A guest earns in Jember and redeems
at Dimsamcong in Banyuwangi; every outlet added makes membership worth more.
That compounding is the thing a franchisee is buying.

Selling the software instead would invert it. A customer would be a competing
operator whose guests' faces and phone numbers would sit in this database
earning points redeemable at this studio — requiring hard tenant isolation on
every table, and making bykami a processor acting for their controller under
PDP, with data processing agreements and breach-notification duties on their
behalf.

**Therefore deleted, not deferred:** `tenant_id`, tenant isolation, and the
Free / Starter / Business / Enterprise subscription tiers. If one operator ever
insists on buying, run them a separate isolated instance and let that rare case
carry its own cost.

The public repo stays fine under this model — the product is the brand and the
operations, not the code. (Note there is no `LICENSE` file, so the default is
all rights reserved.)

## Payment at the kiosk — reversed

**This section previously said payment at the kiosk was dropped. It is not.**
The reasoning below is kept because the objection it raised is still real and
the design has to answer it.

The original argument: nobody pays per session at this studio — `BOOKING TANPA
DP`, the customer pays a human at the counter — so per-session payment is
Sukhakala's unattended mall-kiosk model, a business bykami does not run. It was
also exactly the state machine `booking-phase2.md` eliminated: a slot held while
a QRIS code races a webhook against a timeout, *"where most self-built booking
systems carry their worst bugs"*.

**What changed: this is a self-service booth.** That argument holds only for an
attended studio, where a human at the counter is what stands between a stranger
and the camera. Remove the human and nothing does — anyone who walks up gets a
free session and a free print. **The payment is the attendant.** So a session
starts at `awaiting_payment` with the shutter locked, and a settled QRIS charge
is what opens it.

The old objection is answered by three properties, not by ignoring it:

- **Nothing is reserved.** There is no slot, no inventory and no hold that can
  expire wrongly — only this booth, and one customer at a time is already a
  database constraint. That removes the race the booking document warned about;
  what is left is a charge that either settles or does not.
- **Settlement is pulled, never pushed.** The booth is at `http://localhost`
  with no inbound path, so a gateway webhook cannot reach it. It polls. A lost
  callback is therefore a slow answer rather than a stuck screen.
- **Expiry is decided locally as well as remotely.** A provider that is slow or
  unreachable must not leave a customer staring at a dead QR code, so the booth
  times the code out itself and shows the countdown — an unexplained expiry
  looks like a broken machine.

Two consequences worth stating:

- **An abandoned session is a state, not a deleted row.** A customer who walks
  away from the QR code has a charge behind them that can still settle at the
  gateway a minute later, so the row it points at has to survive. Only unpaid
  sessions can be abandoned; a paid one is closed.
- **QRIS still means Xendit, which is still blocked** on a business entity, NPWP
  and a bank account. Until that exists the only provider is a simulated one
  that takes no money, selected by an explicit flag and announced at startup and
  on screen. A booth with no provider configured refuses to start a session and
  says *"bayar di kasir dulu"*, which is the pre-booth studio working normally.

Booking-time QRIS is unaffected; `booking-phase2.md` still owns it.

## Templates over AI

The gap against Captura is **99+ templates versus six**. Templates are what
customers actually choose from, and on a franchise they are the shared asset
every outlet consumes. That is content work, not engineering, and it is worth
more than any filter.

Frame artwork already exists — booth packages advertise "free desain frame" —
so **import the existing flat files rather than building an authoring tool**.
That also sidesteps the logo-vector blocker in `assets-needed.md`.

**Built, as a catalogue rather than an editor.** An operator uploads a PNG at
`app.bykami.id`; the sheet size comes from its dimensions and the photo cells
come from its transparent regions, so nothing about a frame is typed in twice.
A rectangle typed next to a picture that already contains it is a chance to
disagree with the picture, and the symptom is a face printed off its slot,
discovered on paper.

Detection is checked against the two house frames, whose cells are written down
independently in their manifests, and it recovers them exactly.

Uploads land unpublished, because inference from a picture needs a person to
look at it — the console draws the detected slots over a checkered preview, so a
hole filled with white reads as artwork rather than as a hole. Booths pull the
published set every five minutes and hot-reload.

**Seasons are dates, not a switch.** A Ramadan frame that has to be turned off
by hand is one that is still on the booth in August, and the person who notices
is a customer. Groups (wisuda, wedding, ulang tahun) are the always-on label
alongside them. Windows are resolved on the server: a shop PC's clock is the
least trusted in the system.

Of the original Phase 3, what survives is what is nearly free:

| Feature | Verdict |
|---|---|
| Pose suggestions | Not AI at all — a picture of a pose on a screen. **Keep** |
| Smart crop | BlazeFace/MediaPipe in-browser, milliseconds. **Keep** |
| Beauty filter | Bilateral filter + landmarks gets most of it. **Defer** |
| Background removal | Genuinely browser-capable. **Deferred** — and it partly cannibalises backdrop choice, which `direction.md` records as a selling point |
| Anime / "Pixar" portrait | **Dropped.** Needs diffusion — tens of seconds on a PC with no GPU — and *Pixar* is Disney's trademark, which is a live exposure on software that gets franchised |

**Colour filters shipped, and they are not AI.** Six looks, each a 4×5 colour
matrix. The matrix is applied in `compose` at 300 dpi from the originals, and
the same numbers are served to the browser for `feColorMatrix` — so the screen
and the paper run one arithmetic on one set of numbers rather than two
approximations. A CSS-only filter would print an unfiltered photo, which the
customer discovers after paying. It is applied per cell, so it never tints the
frame the designer drew.

**Check the weights' licence, not the code's.** U²-Net is Apache 2.0; BRIA's
RMBG-1.4, the model everyone reaches for first, is CC BY-NC — non-commercial,
and a franchise ships commercially to every outlet.

**Latency rule:** a customer at a booth tolerates a few seconds. Anything slower
belongs in the gallery as a post-process, never in the kiosk flow.

## Repo layout

Monorepo, extending what exists:

```
api/          Go — cloud monolith (exists)
agent/        Go — booth binary, embeds the kiosk build (exists)
apps/kiosk/   Vite + React → builds into agent/internal/httpd/dist (exists)
sites/*       Astro marketing (exists)
packages/*    shared TS (exists)
infra/        Terraform + Ansible (exists)
```

`apps/*` is in `pnpm-workspace.yaml`. **React + Vite, not Astro** — Astro's
zero-JS-by-default is exactly wrong for a surface that is nothing but
interaction.

`agent/` is a **separate Go module** from `api/`, not a package inside it. They
share no code: the coupling is the versioned `/v1` path, and one module would
quietly make it a compile-time one — while outlet #1 is running a six-month-old
binary by the time outlet #3 exists. `go.work` at the repo root is for editors
and `go test ./...`; CI builds each module from its own directory.

The one thing they do duplicate is phone normalisation, copied into
`agent/internal/httpd/phone.go`. It cannot be imported across modules, and the
rules must not drift — the number *is* the account, so two spellings are two
balances.

**CI ordering matters:** the kiosk bundle must build *before* `go build` embeds
it, or the binary ships yesterday's UI. `.github/workflows/agent.yml` runs both
in one job for that reason, triggers on `apps/kiosk/**` as well as `agent/**`
because a UI change *is* an agent change, and asserts the bundle exists before
compiling. `agent/internal/httpd/dist/` holds a committed `.gitkeep` so the
embed pattern resolves on a clean checkout without a Node toolchain; a binary
built without the UI serves an error page saying so rather than a 404.

## Sequencing

The kiosk is the only major workstream owned end to end. Booking is blocked on
the bookable-resource count; QRIS on Xendit, which is blocked on a business
entity, NPWP and a bank account; brand work on the logo vector. The kiosk needs
a PC and code.

**So build the kiosk with available hours, and start the blocked items on day
one** — they cost no build time and every week they don't start is a week the
long pole isn't moving. `booking-phase2.md` already says of Xendit: "Days to
weeks, entirely outside the build. Start it early; it is the long pole."

Scope realism: agent (folder watch, segmentation, print with status, SQLite, R2
upload, purge, embedded UI), kiosk UI (frame pick, review, print, phone capture,
QR), API (signed URLs, gallery records, admin), gallery renderer. The original
plan said 1–2 months for less than this. Solo, alongside three operating
businesses, expect roughly double.

## What is built

`agent/` and `apps/kiosk/` exist and run end to end on a laptop with a webcam
and simulated payment and printer backends. See `agent/README.md`.

| | |
|---|---|
| Session lifecycle | `awaiting_payment` → `open` → `closed`, plus `abandoned`. One live session at a time, enforced by a partial unique index |
| Payment gate | QRIS charge, polled to settlement, shutter locked until it settles |
| Hot-folder ingest | Size-stable **and** JPEG EOI checked before reading; atomic move; crash-recovery rescan keyed on content hash |
| Session attribution | Filesystem mtime, 20-second grace window for stragglers, orphans kept |
| Take limit | Enforced at capture, because the app owns the shutter |
| Compose | 300 dpi sheets from the originals, fill-and-crop cells, PNG overlay |
| Print queue | DNP DS-RX1HS timings, media as an append-only ledger, interrupted jobs failed on restart with a reason |
| Delivery | Phone captured unverified with two separate unticked consents and a stored consent version |
| Derivatives | A background worker builds the long-edge-2048 copy of every frame; the review screen serves it, not the original |
| Retention | 7-day purge of originals, derivatives **and composed sheets** |

Three things are simulated and each needs an explicit flag that warns at
startup: payment (`-payments=sim`), printing (`-printer=sim`) and capture
(`-source=webcam`). None is a product tier; each is a stand-in for hardware or
an account that does not exist yet.

### The screen flow

`attract → packages → pay → frame → capture → review → delivery → done`, held as
one value in `App.tsx` rather than in a router. The booth has no URLs: a
customer cannot navigate, cannot go back, and must never reach a screen out of
order. On load the step is derived from the server's session state, so a
refresh, a browser restart or a power cut resumes where the customer was rather
than dropping them at the start of a session they have already paid for.

Two of these are the booth's resting behaviour rather than steps. **Attract** is
what the panel shows with nobody in front of it, and is where the 45-second
walkaway timeout on the price list and the 12-second reset after the thank-you
both land. The timeout is deliberately only armed before payment: a timer that
resets a paid session takes money and gives nothing back.

**The frame is chosen before the camera opens**, and again at review. The number
of cells is what decides how many photos the session needs, and a customer who
learns that after shooting has been told too late — so `capture` can say "this
frame needs 4, you have 2". The package still names a default, so tapping
straight through gets the frame the price list advertised, and changing your
mind at review stays free because the template travels with the print request
rather than being committed earlier.

**Camera preparation is a phase of `capture`, not its own screen.** Opening a
webcam is the longest single wait in the flow and it belongs to the driver;
giving it a separate route would mean opening the camera twice and paying that
cost twice. It has three states — opening, live, failed — and `failed` is
distinct on purpose: a booth that says "preparing…" forever reads as a slow
machine rather than a broken one, and nobody calls staff. It is reached by a
timeout as well as by a rejection, because `getUserMedia` with no device
attached does not always fail — it simply never settles.

**The photo session runs itself.** One tap starts it and the booth fills the
whole strip: countdown, shutter, a hold long enough to change pose, repeat, then
straight to review. A tap per frame requires somebody within reach of the
screen, which is the opposite of standing where the camera can see them — and it
is not what the format has ever done. The first countdown is 5 s and the rest
are 3 s: only the first is spent walking back into frame, and five seconds
between every shot is a queue forming behind them.

It stops on the first failure rather than carrying on, because firing three more
times into a camera that just refused produces three more errors. "Berhenti" is
reachable throughout, and the waits are polled in slices so it lands within a
tick rather than at the end of a countdown. The tethered path keeps its
single-shot button: there is no shutter to drive there yet.

Against the customer-facing flow the owner specified, what is **not** built is
`download QR` and `reprint`. The QR has nothing behind it until upload and the
gallery exist, which is gated on the residency fork. Reprint is an open
commercial decision, not an implementation gap — see below.

### The derivative is a performance decision, not only a delivery one

`internal/derive` was written for the gallery — long edge 2048, EXIF stripped,
~600 KB against a 6–10 MB original — and then never called. `derived_path` was
always empty, so the branch in `servePhoto` that prefers it never ran and the
review screen painted **full-resolution originals into 9rem thumbnails**.

Measured on a 24 MP frame: 13.2 MB served per thumbnail, now 580 KB. Across a
15-take filmstrip that is ~200 MB and 360 megapixels of decode, on the one
screen the customer is actively tapping through.

It runs in a background worker rather than inline in the capture handler,
because `File` costs a few hundred milliseconds on a 24 MP original and the
capture handler is on the shutter path — the one place in the booth where
latency is the product.

Derivatives live under `derived/`, **not** beside their originals. `Recover`
walks `sessions/` and records every JPEG without a row, so a derivative filed
next to its original comes back as a second photo on the next restart and
duplicates every frame in the filmstrip. There is a test for exactly that.

### Where the booth's smoothness is actually decided

In the browser, almost entirely. The agent is on loopback; the two things a
customer waits for — the camera opening and `canvas.toBlob` encoding the frame —
never reach it. `?perf=1` puts those timings on the screen, which is where they
have to be: a booth is tested standing in front of one, and a phone has no
devtools window.

A consequence worth stating plainly: **measuring the agent on a server measures
the fastest part of the system.** There is a tunnelled test deployment
(`ansible/roles/booth`, `booth_test_enabled` in `infra/cloudflare/`) but its
value is that `getUserMedia` needs a secure origin, so the flow can be tried on
a phone. It is not a performance environment, and the booth will never run on a
server.

## Open

- **Input device.** Touchscreen, or mouse and keyboard? Undecided, and it
  changes every screen in the UI. The countdown is five seconds, which is the
  answer that survives either choice.
- **Staff auth at the kiosk** — does a session start self-serve, or does staff
  unlock it? Payment now answers most of this: the shutter is locked until a
  charge settles, so an unattended booth is no longer a free booth. What remains
  is whether staff need a way in for reprints and refunds.
- **Upload to R2 and the gallery renderer are not built.** The agent captures,
  composes, prints and purges; nothing leaves the booth PC yet, so the QR
  download in the flow above has nothing behind it. This is the largest
  remaining gap and it is gated on the residency fork.
- **Media is loaded from a subcommand**, `bykami-agent media load 700`, not from
  the touchscreen. Deliberate: everything else at `http://localhost` is defended
  only by the machine being the boundary, and inflating the media counter is the
  one operation where a hostile page could cause real damage — it disables the
  "not enough paper" refusal, and the next customer's strip stops halfway.
- **What the remote shutter physically is.** Wired, IR or RF is fine; the
  Camera Connect phone app over WiFi is mutually exclusive with USB tethering
  and would need replacing with an RS-60E3. Last unverified assumption in the
  capture path.
- **Unattended recovery** — auto-login, agent and Chrome auto-start on boot,
  Assigned Access configured, and Windows Update held to active hours with
  deferred feature updates. A power cut must not need a human, and neither must
  a Patch Tuesday.
- **Agent liveness** — heartbeat to `api/`, or nothing knows the booth is down.
- **Agent SQLite backup.** It holds phone numbers and consent records; the VPS
  has a backup story and the booth PC does not.
- **Timezone.** `Asia/Jakarta` on the booth PC and in the API — session
  boundaries and 30-day expiry both depend on it.
- **Printer error mid-session** — reprint policy, and whether a failed print is
  refunded.
- ~~Reprint~~ — settled. **The paid copies are handed out one at a time.** A
  package including two prints releases the first on "Cetak", then offers
  "Cetak lagi (1 tersisa)", which is what a pair splitting a strip actually
  wants. No money rule changed and no second payment flow.

  This moved the allowance check from per-request to cumulative, and that was
  load-bearing rather than cosmetic: while the browser asked for all copies at
  once, `copies > print_copies` was a sufficient backstop. Handing them out one
  at a time makes every individual request look legal, so the server now sums
  what the session has already claimed (`Queue.CopiesForSession`, failed jobs
  excluded) and `prints_done` is served in the session view — counted in the
  browser it would reset on a refresh.

  What is *not* built is the staff-unlocked reprint for a smudged sheet, which
  is the industry meaning of the word and still needs the kiosk staff auth open
  above.
- Frame spec: bleed, safe area, DPI, cut marks for strips.
- Privacy policy page must exist before the first number is collected.
- ~~`identity.go:8` still claims `.bykami.id` scoping in a comment~~ — settled.
  The JSON API is bearer-only and the operator console's cookie is `__Host-`
  prefixed and host-only. See `design/platform-architecture.md`.
- **The catalogue is duplicated.** `agent/internal/catalog/packages.json` copies
  prices from `packages/content/src/verticals/studio.ts` because a Go binary on
  an offline Windows PC cannot read TypeScript. A test fails when the two
  disagree, but the prices themselves are still marked *unverified* upstream —
  read off a PDF and never confirmed with the owner. Confirm them once, in
  `studio.ts`.
- **Which template belongs to which package** is currently a guess in that same
  file. MINI and MIDI open on a 3-frame strip, MAXI and BIG MAXI on a 4-frame
  one; nothing in the price list says so.
