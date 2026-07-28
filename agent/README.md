# agent — the booth binary

One Go process on the studio PC. It watches the camera's hot folder, owns the
print queue, serves the kiosk UI at `http://localhost`, and deletes the
originals seven days later. Chrome in kiosk mode is the only other moving part.

A **separate module** from `api/`, not a package inside it — see
`design/kiosk.md` → Repo layout for why, and for why the two duplicate phone
normalisation rather than sharing it.

| Package | Owns |
|---|---|
| `internal/store` | SQLite, pragmas, embedded migrations |
| `internal/session` | The booth lifecycle, attribution, the take limit |
| `internal/payment` | The QRIS charge that unlocks the shutter |
| `internal/photo` | Every frame the booth has taken. Rows outlive files |
| `internal/ingest` | Hot-folder watcher, debounce, crash recovery |
| `internal/compose` | Frames → a printable sheet at 300 dpi |
| `internal/printer` | The print queue and the media ledger |
| `internal/derive` | The delivered file: long edge 2048, EXIF stripped. A background worker builds one per frame |
| `internal/purge` | The 7-day deletion of originals and sheets |
| `internal/catalog` | What the customer can buy |
| `internal/httpd` | The local API, and the embedded kiosk UI |

## Run it on a laptop

Everything works with no camera, no printer, no payment account and no booth PC:

```bash
pnpm --filter @bykami/kiosk build     # must run BEFORE go build
go run ./cmd/bykami-agent \
  -root /tmp/booth -source webcam -payments sim -printer sim -sim-print-speed 1000
go run ./cmd/bykami-agent -root /tmp/booth media load 700 "roll 1"
```

Then open `http://127.0.0.1:8899`. Pick a package, press **Simulasikan
pembayaran** on the QR screen, and the shutter unlocks.

Add `-sim-auto-settle 1s` and the charge settles itself, which skips the QR
screen entirely — useful when the thing being tested is the capture flow rather
than the payment one.

### What the review screen shows

The sheet as it will print, composited in the browser: background, photos,
overlay, in the order `compose.Sheet` draws them, positioned as percentages of
the template's own 300 dpi geometry. `object-fit: cover` is the CSS equivalent
of `drawCover`, so the preview and the printed sheet agree by construction
rather than by two crop implementations being kept in step by hand.

**Not a server render, deliberately.** Composing the real sheet is a 300 dpi job
that takes seconds, and a customer taps between templates; the file the printer
receives is composed once, when they commit.

`print_dpi` follows the template being looked at rather than the one the package
opened on. A 4R cell is 1200×1800 where a strip cell is 540×360, so the same
frame is 300 dpi in one and 266 in the other — reading the package's template
here suppressed a genuine below-300-dpi warning after the customer switched.

### The design language, and the two rules that are structural

Scandinavian minimalism with flat cartoon illustration: a five-colour palette
(deep red, emerald, cream, blush, mustard) over near-black ink, thick outlines,
generous radii, botanical doodles.

Two of its rules — **no gradients** and **no glossy effects** — are the ones
easiest to reintroduce by accident months later, so they are enforced by the
tokens rather than by discipline. `--shadow` is a *hard* offset,
`4px 4px 0 var(--ink)`, not a blur. Anything that reaches for it gets flat depth
it cannot soften, and pressing a control translates it into its own shadow
instead of fading or glowing. There is no gradient token to reach for at all.

Spacing comes from the `--s-*` scale and nothing else. Outlines come from
`--stroke` (2px) and `--stroke-thick` (3px).

### The typefaces are named, never fetched

Two families: a sans for everything, and a handwritten face for single accent
words (`.hand`). `styles.css` names both as *local* preferences with the system
stack behind them, because a booth PC is offline-first and a font request that
hangs is a screen that hangs.

The sans candidates — Pretendard, Plus Jakarta Sans, Manrope, Inter — are all
SIL OFL, so bundling one self-hosted woff2 is licence-clean and would settle it,
at the cost of a couple of hundred kilobytes in the binary. Not done yet.

The handwritten stack is weaker: it resolves to Segoe Script on Windows, which
is what the booth runs, and to Bradley Hand on a developer's Mac. Neither is
redistributable, so this one *needs* a bundled OFL face to be certain rather
than merely likely. Until then a machine with neither installed falls back to
the sans and the accent simply stops being an accent — which is the right way
for a decorative font to fail.

### Illustration

`src/assets/selfie.svg` is from [Open Doodles](https://www.opendoodles.com),
CC0, recoloured onto the palette — see `src/assets/README.md` for exactly what
was changed. The botanical marks in `src/Doodle.tsx` are drawn inline as SVG
paths for the same reason there is no webfont request: a decoration that arrives
over the network is a decoration that can fail to arrive.

### Measuring it

`?perf=1` puts the client-side timings on the screen and keeps them for the tab:
camera cold start, JPEG encode, upload, the state refresh, total shutter time,
and how long the filmstrip took to paint. On the screen rather than in a console
because a booth is tested standing in front of one, and a phone has no devtools
window.

Almost all of the booth's smoothness is decided in the browser. The agent is on
loopback, and the two things a customer actually waits for — the camera opening
and `canvas.toBlob` encoding the frame — never reach it. `?perf=0` turns it off.

## The three simulations, and why each needs a flag

None of these is a product tier. Each stands in for hardware or an account that
does not exist yet, and each is opt-in with a startup warning for the same
reason `api/` requires `-otp-delivery=log`.

| Flag | Stands in for | What it would cost to leave on |
|---|---|---|
| `-payments=sim` | Xendit QRIS, blocked on a business entity, NPWP and a bank account | Every session free. The screen can unlock itself |
| `-printer=sim` | DNP's Windows driver and SDK | Nothing comes out of the machine |
| `-source=webcam` | A tethered Canon 200D | ~180 dpi at 4R — visibly softer than what the studio delivers today |

With no `-payments` at all the booth refuses to start a session and says *"bayar
di kasir dulu"*. That is the deployed default, and it is the pre-booth studio
working normally rather than a broken booth.

The `payment/simulate` route **does not exist** unless `-payments=sim` is set —
it is a 404, not a guarded handler, so a booth that could take real money has no
route that skips paying.

## The flow

```
pilih paket → QR → (settle) → countdown → capture → pilih foto → cetak → nomor → selesai
                 ↑ shutter locked here
```

| | | |
|---|---|---|
| `GET` | `/api/state` | Packages, templates, session, payment, media, consent version |
| `POST` | `/api/session` | Choose a package. Mints a QRIS charge |
| `POST` | `/api/session/cancel` | Abandon an **unpaid** session |
| `POST` | `/api/session/close` | Finish a paid one |
| `GET` | `/api/payment` | Poll for settlement. Opens the shutter when it lands |
| `POST` | `/api/capture` | Fire the shutter, or accept a webcam frame |
| `GET` | `/api/photos` | This session's frames, in capture order, with print dpi |
| `POST` | `/api/print` | Compose a sheet and queue it |
| `POST` | `/api/delivery` | Phone number plus two separate consents |

## Things that are easy to get wrong and are therefore tested

- **A file that exists is not a file that is complete.** Vendor software writes
  progressively, so ingest waits for the size to be stable *and* for the last
  two bytes to be the JPEG EOI marker. Size alone is not enough: a writer that
  pauses looks finished.
- **Frames taken in the same second.** `captured_at` has one-second resolution
  and a photobooth fires faster than that. The tiebreak is insertion order, not
  the random id — otherwise the customer's strip comes out shuffled.
- **Two strips come off one 4×6 sheet.** Counting copies instead of sheets makes
  a roll appear to last half as long as it does.
- **A failed print consumes no media.** Over-counting the roll makes the operator
  load early; under-counting makes them run out mid-session.
- **A crash between the rename and the insert.** The file moves before the row is
  written, deliberately: that leaves a file with no row, which the startup
  rescan repairs. The other order leaves a row pointing at nothing.
- **The same bytes twice.** Content-addressed, so a rescan cannot duplicate and a
  duplicate frame is reported rather than silently counted as a take.

```bash
go vet ./...
go test -race -count=1 ./...
```

`-race` because the print queue drains jobs on one goroutine while the HTTP
handler submits them on another. That is the normal case here, not an edge one.

## Security posture

**Anything running on the booth PC can drive this API, and adding a token would
not change that** — the token would have to be readable by the UI on the same
machine. The real boundary is the machine: Assigned Access, auto-login and a
locked-down Windows session. Pretending otherwise in code would be theatre with
a maintenance cost.

What *is* defended is the gap a localhost bind leaves open: a page on the public
internet reaching `http://localhost` in the operator's browser. Any website can
POST there, and DNS rebinding turns an attacker's hostname into `127.0.0.1` so
even same-origin checks pass. Two cheap checks a real kiosk browser satisfies
and a hostile page does not — the `Host` header must name localhost, and a
cross-site `Origin` is refused.

### The one exception: a tunnelled test deployment

`-public-host <hostname>` makes the agent answer to one hostname besides
localhost, and `-access-token` (or `BYKAMI_ACCESS_TOKEN`) gates it. **Setting the
first without the second is refused at startup** — every route here is
unauthenticated by design, which is correct for a screen wired to the PC beside
it and indefensible on a public hostname where `/api/capture` accepts 16 MB
uploads and writes them to disk.

It exists for one reason: `getUserMedia` refuses to run on an insecure origin, so
the capture flow cannot be tried on a phone or a tablet without real HTTPS and a
real hostname. A booth in a shop sets neither flag.

The token is accepted once from `?t=`, then moved into an `HttpOnly; Secure`
cookie so it stops appearing in the address bar. Requests arriving over
localhost still need no token — demanding one there would be theatre, since
anything on that machine can read it.

`ansible/roles/booth` deploys this behind the tunnel; it is off unless
`booth_enabled` is true, and the token comes from a mode-0600 `EnvironmentFile`
rather than the unit file, which is world-readable.

Loading media is **not** an HTTP route for this reason. It is the one operation
where a hostile page could do real damage without touching the booth: inflating
the counter disables the "not enough paper" refusal, and the next customer's
strip stops halfway with no warning.

```bash
bykami-agent -root /var/booth media status
bykami-agent -root /var/booth media load 700 "roll 1"
bykami-agent -root /var/booth media adjust -5 "jam, five sheets wasted"
```

An adjustment without a reason is refused. The ledger is append-only, exactly as
the loyalty ledger in `api/` is, and for the same reason: the most likely author
of an `UPDATE` here is a well-meaning fix for "the counter looks wrong".

## Retention

Seven days, and it does not depend on anyone remembering. Originals, delivered
derivatives **and composed sheets** — the sheets hold the same faces, laid out
for the printer, so retention that covered only the originals would leave a
full-resolution copy of every customer on the machine indefinitely.

The photo rows stay. They are how the agent knows not to re-ingest bytes it has
already seen, and how a question asked three weeks later is answerable once the
pixels are gone.

## Not here yet

- **Nothing leaves the booth PC.** R2 upload, the gallery renderer and the QR
  download have no implementation, so the delivery screen captures a number and
  a consent and does nothing with them yet. Gated on the residency fork in
  `design/infrastructure.md`.
- **No shutter release.** `-source=hotfolder` announces the countdown and the
  frame is fired by hand. The recommended path is a USB relay into the RS-60E3
  jack — the last open question in the capture design.
- **No DNP backend.** `-printer=sim` is the only one that exists.
- **No liveness heartbeat to `api/`**, so nothing knows the booth is down.
- **No OTA updates.** Releases are published by CI and installed by whoever is
  standing at the booth.
