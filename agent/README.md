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
| `internal/clip` | The moving version of a frame: five seconds of camera, rendered to an animated GIF by a background worker |
| `internal/purge` | The 7-day deletion of originals, sheets and clips |
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

**The live camera preview is masked to the same cell.** A webcam hands over 16:9
and a frame's holes are usually taller than they are wide, so `drawCover` throws
the sides away — and a preview showing the whole sensor is a preview showing
pixels that will never be printed. People frame themselves against the edges
they can see and then find the print cropped into their shoulders. The preview
box takes its aspect ratio from the template's first cell and the video fills it
with `object-fit: cover`, which is the same crop; the ink letterbox around it is
that crop, drawn. The filmstrip thumbnails follow the same rule.

Nothing is discarded at *capture* — the file on disk is still the full frame, so
switching frame at review re-crops from everything the camera saw rather than
from an already-cropped copy.

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
was changed. The marks in `src/Doodle.tsx` are drawn inline as SVG paths for the
same reason there is no webfont request: a decoration that arrives over the
network is a decoration that can fail to arrive.

The set is eight marks — two botanicals, a sparkle, a heart, a rainbow, a cloud,
a camera and a squiggle — because it was two, both on the attract screen, and
the six screens behind it therefore turned into a form the moment a customer
paid. One mark per screen heading, absolutely positioned so it costs no layout,
plus colour on the package cards: four identical cream rectangles is a table,
and a table is what the booth should least resemble on the screen where somebody
decides to spend money. Strictly the five palette tokens — a fifth package
restarts the cycle rather than introducing a sixth colour.

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

`-source=hybrid` is not on that list. It is a production mode, not a stand-in:
the preview comes from the camera's virtual-webcam feed so the customer can see
themselves pose, and the frame that gets printed still arrives through the hot
folder at the sensor's own resolution. Nothing is ever captured from the preview
— see the capture handler, which answers `202 awaiting_file` on every tethered
source even when the browser posts bytes.

With no `-payments` at all the booth refuses to start a session and says *"bayar
di kasir dulu"*. That is the deployed default, and it is the pre-booth studio
working normally rather than a broken booth.

The `payment/simulate` route **does not exist** unless `-payments=sim` is set —
it is a 404, not a guarded handler, so a booth that could take real money has no
route that skips paying.

## The flow

```
pilih paket → QR → (settle) → pilih frame → sesi foto → pilih foto + filter → cetak → QR download → selesai
                 ↑ shutter locked here      ↑ automatic, one countdown per frame        ↑ nomor optional here
```

**The photo session runs itself.** One tap starts it; the booth counts down,
fires, holds the shot long enough to change pose, and repeats until the strip
is full, then goes to the review screen on its own. A tap per frame would mean
somebody standing within reach of the screen, which is the opposite of standing
where the camera can see them. The first countdown is longer than the rest
because it is the only one spent walking back into frame.

The tethered paths keep a single-shot button until something can fire the
camera. With `-shutter` set they run the same automatic sequence as the webcam.

## Firing the camera without the relay

`design/kiosk.md` recommends a USB relay into the RS-60E3 jack, and that is
still the sturdier answer — its failure mode is a plug coming out, which is
visible and costs a few dollars to fix, rather than a vendor update quietly
changing an API. But the relay is hardware that has to be bought and wired, and
the booth without it makes a customer press the camera by hand.

`-shutter` is a URL the agent calls when the countdown ends:

```bash
-shutter 'http://127.0.0.1:5513/?slc=capturenoaf'   # digiCamControl
```

A URL and not a vendor integration, for the same reason the hot folder is a
directory: whatever already owns the camera can be asked to fire it, and the
booth learns no driver. No cgo, no EDSDK, no registration — one `http.Get`.

digiCamControl serves this on port 5513 and both triggers *and* tethers, so it
replaces EOS Utility rather than joining it — one moving part instead of two.
Point its session folder at `-hot-folder` and the frame lands where the watcher
is looking. Use its **HTTP API and not the bundled CLI**: `CameraControlCmd.exe`
refuses to run while the GUI is open, even minimised, and the GUI is what does
the tethering.

`capturenoaf` fires without autofocus, which is usually what a booth wants — a
camera that hunts for focus against an empty background can decline to fire at
all, and a countdown that ends in nothing is the failure this whole path is
trying to avoid.

**The reply is read, not just the status code.** digiCamControl answers `200`
and puts the failure in the body: "no camera is connected" arrives as a
perfectly successful HTTP response, so a booth trusting the status alone would
count down, photograph nobody, and report that everything went well. Anything
that is neither empty nor `OK` is treated as a refusal — which errs towards a
false alarm on some future tool with a chatty body, and that is the right way
round. A booth that wrongly says "call staff" is annoying; one that wrongly says
nothing is wrong sends people home without their photographs.

A refused shutter is a `502` and the customer is told to call staff. It is never
a take: nothing is billed for a frame the camera did not expose.

## Which camera the booth previews

A booth PC has two cameras: the tethered one the customer is photographed by,
and the webcam in the lid. `getUserMedia` with no device named hands over
whichever the browser calls default, and that is reliably the lid one — so the
screen shows a laptop webcam while the Canon photographs the same person from a
different angle, and nothing looks wrong until the prints come out.

`-camera` names the right one as a case-insensitive substring of the device
label: `-camera EOS` picks "EOS Webcam Utility (Canon EOS 200D)" out of a
machine that also has "Integrated Camera". A substring and not a device id
because ids are useless here — the browser mints them per profile, they rotate
when the profile is cleared, and nobody can read one off a machine to put it in
a service definition. A label fragment survives a reinstall and can be written
down.

Finding it costs two `getUserMedia` calls, and that is the permission model
rather than a choice: device labels are empty until the page holds a camera
permission, so there is no way to find the camera called "EOS" without first
opening *a* camera. The first stream is released before the second is asked for,
because a camera is exclusive on Windows and holding the lid webcam while asking
for the Canon is how a booth gets `NotReadableError` on the device it wants.

`-source=hybrid` refuses to start without `-camera`. The two cameras look
identical on screen, so a booth that previewed the wrong one would look like it
was working until somebody compared a print to what anybody was looking at.

| | | |
|---|---|---|
| `GET` | `/api/state` | Packages, templates, session, payment, media, consent version |
| `POST` | `/api/session` | Choose a package. Mints a QRIS charge |
| `POST` | `/api/session/cancel` | Abandon an **unpaid** session |
| `POST` | `/api/session/close` | Finish a paid one |
| `GET` | `/api/payment` | Poll for settlement. Opens the shutter when it lands |
| `POST` | `/api/capture` | Fire the shutter, or accept a webcam frame |
| `GET` | `/api/photos` | This session's frames, in capture order, with print dpi |
| `POST` | `/api/print` | Compose a sheet and queue it. Carries the template and the filter |
| `POST` | `/api/delivery` | Phone number plus two separate consents. Optional |
| `GET` | `/g/{token}` | The customer's download page. No booth token — see below |
| `GET` | `/g/{token}/p/{id}` | One photo, unframed. `?dl=1` sends it as an attachment |
| `GET` | `/g/{token}/s/{job}` | One composed sheet — the framed version, as printed |

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
- **A filter that only exists on screen.** The obvious build is a CSS filter on
  the preview, which prints an unfiltered photo — the customer finds out on
  paper. `compose` applies it, from the originals, and a test composes a real
  sheet and reads its pixels.
- **A filter that colours the frame too.** The matrix is applied per cell, not to
  the sheet, so the designer's artwork keeps the colours they chose.

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

**`-access-token` takes a comma-separated list, so testers can hold one each.**
There is no endpoint that mints them: a token vending machine on the
unauthenticated side of this server would hand out the booth, and these are
deploy-time secrets read once at startup from a mode-0600 `EnvironmentFile`.
Generate them with `openssl rand -hex 24`.

The list is what makes access revocable. With a single shared secret,
withdrawing one person means rotating for everybody — which in practice means
nobody is ever withdrawn. The cookie carries the token that matched rather than
the list, so dropping one token from the vars file and re-running the play stops
recognising exactly that person's cookie and leaves the rest working. Tested.

Every configured token is compared, with no early return on a match: stopping
early would make the time taken depend on which token was presented, which is
the leak the constant-time compare exists to close.

**`/g/…` is exempt, and has to be.** That token is the *booth's* — it opens
`/api/capture` and `/api/print` — so handing it to a customer to collect their
photographs would hand them the booth. The download gallery carries its own,
much narrower secret instead; see below. The exemption matches the gallery
routes exactly rather than the `/g/` prefix, because anything the mux cannot
route falls through to the kiosk UI, and a prefix test served that UI
unauthenticated to `/g/`, `/g/x/y/z` and every other near-miss. There is a test.
Adding a fourth `/g/` route means teaching `isGalleryPath` about it too.

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

## The download, and why the booth serves it

`GET /g/{token}` is the page the delivery QR points at: one session's photos, as
a phone can save them. `?dl=1` on either file route adds a `Content-Disposition`
so iOS Safari saves rather than displays it.

**Both versions are offered, because they are different pictures.** `/p/{id}` is
the frame as the camera saw it. `/s/{job}` is the composed sheet — the customer's
photos inside the template they picked, with their filter, at 300 dpi. A booth
that handed back only the loose frames would be giving less than the print they
are holding, and one that handed back only the sheet would have cropped every
photo to fit a cell and thrown the rest away.

The sheet is the file that went to the printer rather than a fresh composition.
Recomposing here would need the template, the filter and the chosen frames
stored somewhere, and would still quietly diverge from the print the moment any
of the three changed. It also happens to be the right file already: composed
from the originals, and re-encoded by `compose`, which writes no EXIF and so
carries no camera serial into whatever group chat the link reaches.

A reprint composes the same picture again under a new filename, so the page
deduplicates by content — most packages include more than one print and most
customers use them, which makes the identical strip appearing twice the ordinary
case rather than the exotic one. Two prints that genuinely differ, a second
template or another filter, both survive. Both behaviours are tested.

**The booth serves this itself, rather than R2 behind `gallery.bykami.id`.** The
cloud version in `design/kiosk.md` remains the right answer for a fleet, and it
is blocked on the residency fork — meanwhile the booth already holds the
photographs, already derives them to 2048px with EXIF stripped, and already
deletes them after seven days. Serving them from here needs no upload, no
bucket, no credential to rotate, and no decision about which country the files
sit in. It is the version that works now.

It needs `-public-host`, because a phone on mobile data cannot reach a booth PC
on a shop's wifi. Without one, `share_url` is empty and the delivery screen
offers WhatsApp alone instead of an unscannable square — which is every booth
that has no tunnel in front of it.

**What guards it is the unguessable URL, and deliberately nothing else.**
Customers paste these links into group chats; that is wanted, and no control
survives it. What makes it defensible is that the capability is narrow and
expires on its own:

- Read-only. Nothing under `/g/` accepts a body.
- One session. The token names a session, the path names a photo or a print job,
  and the two are compared — without that a single valid token would open
  everything the booth has ever taken or composed. Tested for both.
- Nothing but pictures. Not the phone number, not the price, not the consent. A
  link forwarded to a group must not carry the customer's number into it. Print
  jobs are read for their sheets alone — not their state, their copies, or what
  they cost the media roll.
- `default-src 'none'` with `img-src 'self'`, and the inline stylesheet is
  hashed into the CSP at startup so the two cannot drift. A photo gallery is
  exactly the page somebody later adds a lightbox to; this is what makes that
  fail loudly.
- `noindex`, `no-referrer`, `same-origin` CORP.

### The moving version

Every frame also keeps the five seconds of camera behind it, delivered as an
animated GIF at `/g/{token}/m/{clip}` and offered as a badge on the still.

**The buffer rolls; it does not start with each countdown.** The first countdown
is five seconds and the ones after it are three, so a recorder started at each
countdown would hand out clips of two different lengths. A ring buffer running
for the length of a strip gives every shot the same five seconds — and on the
later shots it catches the tail of the one before, which is people reacting to
the frame they just took and is the best footage in the session.

**GIF, and it is a considered choice.** This page has no JavaScript and a
`default-src 'none'` policy, so an animated GIF is the only moving format that
needs neither a script nor a wider CSP. It is also the only one a phone reliably
saves: long-pressing an animation the browser is *displaying* offers "Add to
Photos". That is why this is the one file route with no `?dl=1` — a
`Content-Disposition` would put it in the downloads folder and break the single
gesture the feature exists for. An MP4 would be a tenth of the size and cannot
be produced here: the binary is cross-compiled with `GOOS=windows`, so cgo is
unavailable and with it every H.264 encoder worth having.

**The frames are not photos, and that is load-bearing.** Fifty frames a shot
through `ingest` would fill the review screen with near-identical thumbnails and
exhaust a fifteen-take session on the second shot, because `MayFire` counts rows
in `photos` against `take_limit`. Clips live in their own table and their own
tree. The burst is also posted *after* the frame rather than with it — it is
twenty times the bytes, and `/api/capture` is on the shutter path.

Rendering is the heaviest background job on the booth, two to three seconds of
one core per clip, so the worker takes one per pass. That still keeps pace with
capture, since a shot costs about five seconds of countdown and hold before the
next one arrives.

**The console error on this page is the policy working.** Cloudflare Web
Analytics is on for the zone, so the edge injects its beacon into every HTML
response and the CSP refuses to run it — which is the right outcome on a page
showing a customer's face, and worth knowing before somebody "fixes" it by
loosening `default-src`. The beacon is injected after the agent has served the
bytes, so nothing here can stop it being added; only the CSP stops it running.

## Retention

Seven days, and it does not depend on anyone remembering. Originals, delivered
derivatives, **composed sheets and clips** — the sheets hold the same faces, laid
out for the printer, and a clip is that face in motion, which is more
identifying rather than less. Retention that covered only the originals would
leave a full-resolution copy of every customer on the machine indefinitely.

Clips are deleted through their row rather than by file age, unlike sheets: a
clip belongs to exactly one photo, so it inherits that photo's deadline exactly
instead of approximating it from a modification time.

The photo rows stay. They are how the agent knows not to re-ingest bytes it has
already seen, and how a question asked three weeks later is answerable once the
pixels are gone.

It is also the download's expiry. There is no second mechanism and no separate
clock: the link stops working because the photos behind it are gone. `-retention`
sets the window, and the number the delivery screen promises is read from the
same value rather than typed into the UI — it said 30 days for a while, beside a
purge that had always deleted at 7.

## Frames come from the cloud

The catalogue lives at `app.bykami.id` (`api/internal/frames`): an operator
uploads a PNG, the console reads its cells out of the transparent regions, and
publishing it puts it on every booth. `-frame-sync https://app.bykami.id` plus
`BYKAMI_BOOTH_TOKEN` turns the pull on; the worker polls every five minutes and
writes designs into `<root>/frames`, swapping the live template set without a
restart.

**It is a cache, not a dependency.** A failed sync leaves the booth offering the
frames it already has — the alternative is a photobooth that stops selling when
a server in Singapore does. A booth with no `-frame-sync` never polls and runs
on the designs compiled into the binary.

Precedence is built-in, then synced, then `-templates`. The local directory wins
last: somebody standing at the booth with a file is making a deliberate decision
about that machine, and the next poll should not undo it.

One shared secret for every booth, not one each. Per-booth tokens would be a
table, an enrolment flow and a revocation story for a fleet that does not exist;
when there is a second outlet, that becomes worth having.

## Not here yet

- **Nothing leaves the booth PC**, and the download now leans on that rather
  than waiting for it — see below. R2 upload and `gallery.bykami.id` still have
  no implementation and stay gated on the residency fork in
  `design/infrastructure.md`.
- **No WhatsApp sender.** The delivery screen can still take a number, and
  storing it is all that happens; `Sender` has no implementation anywhere. The
  QR is what actually delivers today, which is why the number stopped being a
  precondition for leaving the screen.
- **No shutter relay.** `-shutter` fires the camera through whatever software
  already owns it, which is what makes the tethered path automatic today.
  Without it `-source=hotfolder` and `-source=hybrid` announce the countdown and
  the frame is fired by hand. The relay into the RS-60E3 jack is still the
  sturdier answer and is still unbuilt.
- **No motion on the hybrid path.** A clip has to be filed against the photo it
  belongs to, and a tethered capture hands back no photo id — the frame has not
  been taken yet, let alone ingested. So `-source=hybrid` shows a live preview
  and keeps no GIF from it; only `-source=webcam` delivers motion today.
- **No DNP backend.** `-printer=sim` is the only one that exists.
- **No liveness heartbeat to `api/`**, so nothing knows the booth is down.
- **No OTA updates on the booth PC.** The shop machine is Windows with no
  inbound anything, so its release is still installed by whoever is standing at
  it. The linux/amd64 build published alongside it *does* update itself — see
  `booth_update_enabled` in `ansible/README.md` — but that is the test VPS, and
  a box reachable through a tunnel it dialled out to is not the problem the shop
  PC has.
- **No per-booth identity.** Frame sync authenticates with one shared secret, so
  a booth cannot be revoked without rotating every booth's token.
