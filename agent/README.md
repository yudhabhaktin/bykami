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
| `internal/derive` | The delivered file: long edge 2048, EXIF stripped |
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
