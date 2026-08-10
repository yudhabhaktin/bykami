# bykami

**A Go monolith, one small box, and a photo booth in Banyuwangi.**

bykami is the platform behind a group of small businesses in Banyuwangi, East
Java: four websites, a customer and loyalty backend, and a self-service photo
booth that takes the payment, runs the session, fires the camera, prints the
sheet, and hands the photographs back over a QR code.

Built by [Yudha Bhakti Nugraha](https://yudhabhakti.com). I build and run the
software; the brand, the prices and the photography are the studio owner's, which
is why parts of this repo are visibly waiting on someone else.

|  |  |
|---|---|
| **Runtimes** | Two Go binaries — one on a 2 GiB VPS, one on a Windows PC in a shop — plus four static sites |
| **Stack** | Go 1.26, SQLite, React + Vite, Astro, Terraform, Ansible, Cloudflare (Pages, Tunnel, R2), Alibaba ECS |
| **Size** | ~11.5k lines of Go, ~7.5k of TypeScript, 1.6k of Terraform and Ansible |
| **Tests** | 282 Go test functions, ~9k lines of them, run under `-race` |
| **Docs** | ~4.5k lines, mostly decision records explaining why rather than what |
| **Target hardware** | Canon EOS 200D, DNP DS-RX1HS dye-sub printer, touchscreen, Windows 11 Pro. Capture and payment currently run against simulators; the printer backend is real but has never met the printer — see `STATUS.md` |

You can run the entire photo booth on a laptop with no camera, no printer and no
payment account. Three commands, in [Running it](#running-it).

## The interesting part

Most of what is worth reading here comes from one constraint: **the mistakes come
out on paper**, in front of a customer who has already paid. There is no error
toast that fixes a face cropped out of a print, and no retry that gives back a
wasted sheet of dye-sub paper. That pushes correctness earlier than a web app
usually needs it, and it is the reason for most of the decisions below.

**A frame is a PNG, and nothing else is typed in.** An operator uploads artwork;
the sheet size comes from its dimensions and the photo slots are flood-filled out
of its own transparent regions. Typing a rectangle next to a picture that already
contains it is a chance to disagree with the picture, and the symptom shows up on
paper. Holes do not have to be rectangles — the artwork is drawn over the photo,
so a heart-shaped cut-out prints as a heart. → `api/README.md`

**The preview and the print agree by construction.** A webcam hands over 16:9 and
a frame's holes are usually taller than they are wide, so the crop throws the
sides away. A preview showing the whole sensor is a preview showing pixels that
will never be printed — people frame themselves against edges they can see and
then find the print cropped into their shoulders. The preview box takes its
aspect ratio from the template's own cell and fills it with `object-fit: cover`,
which is the CSS equivalent of the Go crop. Two implementations that agree
because they are the same rule, not because someone keeps them in step.
→ `agent/README.md`

**A file that exists is not a file that is complete.** Vendor tethering software
writes progressively, so the hot-folder watcher waits for the size to be stable
*and* for the last two bytes to be the JPEG end-of-image marker. Size alone is
not enough: a writer that pauses looks finished. → `agent/internal/ingest`

**The booth keeps selling when the cloud is down.** Frames sync from
`app.bykami.id` every five minutes, and a failed poll leaves the booth offering
what it already has. Treating that sync as a dependency would mean a photo booth
that stops taking money because a server in Singapore does. → `agent/internal/framesync`

**Two groups cannot hold the same room, and a unique index on the start time
would not have been enough.** Booking reserves one row per half hour a session
occupies, keyed `(resource_id, starts_at)`, inserted in the booking's own
transaction — so a three-hour shoot at 09:00 and a one-hour one at 10:00 collide
rather than both being accepted on the strength of different start times. Nothing
in Go decides this: availability is read, shown to somebody, and acted on minutes
later, so every check before the insert exists to produce a good error message and
the database has the only vote that counts. → `api/internal/booking`

**Google Calendar is the calendar the owner already works in, reached with a
service account rather than OAuth.** An unverified Google project issues refresh
tokens that expire after seven days, so the consent-screen version of this works
for a week and then stops silently — which is a booking page that quietly stops
knowing when the studio is busy. A shared calendar and a key that does not rotate
has no such clock. The busy ranges it reads are a cache and never a source: a
failed poll leaves the studio selling from what it already knows, because a studio
that stopped taking bookings when an API in Mountain View got slow would have
traded a real sale for a hypothetical conflict. → `api/internal/gcal`

**Auth answers 503 on the deployed box, deliberately.** Data residency is
unresolved and the only OTP sender that exists writes codes to a log, so the gate
is enforced in code rather than remembered — and it runs *before* the service, so
a request behind it writes no challenge row that would still be redeemable once
the gate opened. It also means nobody can log into the operator console, which is
why the frame catalogue is a shell subcommand too. → `api/README.md`

**The customer's download page has no JavaScript and a `default-src 'none'`
policy**, with the inline stylesheet hashed into the CSP at startup so the two
cannot drift. Cloudflare's analytics beacon gets injected by the edge and blocked
by the policy — the console error on that page is the policy working, on a page
showing a customer's face. The moving version of each shot is an animated GIF
because it is the only format that needs neither a script nor a wider policy, and
because the binary cross-compiles to Windows without cgo, which rules out every
H.264 encoder worth having. → `agent/README.md`

**CI reaches into no machine.** The VPS has one inbound rule and the shop PC has
none, so both boxes poll GitHub Releases and install their own updates —
verifying a checksum, installing atomically, then rolling back if the health check
fails. The booth's updater also defers while a customer is mid-session, and treats
a booth that will not answer as busy rather than idle. → `ansible/README.md`

## What the business is

bykami is several local businesses under one brand, not one business with a
website. Four verticals, three of them trading today:

| Vertical | What it sells | Site |
|---|---|---|
| **studio by KAMI** | Self photo studio, photobox, pas foto, at a fixed address in Jajag | `studio.bykami.id` |
| **booth by KAMI** | A photo booth that travels to events across Banyuwangi, Jember and Bondowoso | `booth.bykami.id` |
| **Dimsamcong** | F&B | `dimsamcong.bykami.id` |
| **photo by KAMI** | On-location photography and video | No site yet |

A customer is meant to be one customer across all of them: one account, one
`#SobatKAMi` loyalty balance, whether they booked a studio session or bought
dimsum. That is why this is a platform rather than four separate builds, and it
is what a new vertical inherits for free.

## What the software is for

Three jobs, in the order they were built.

**Be findable.** Most customers arrive through search or Instagram. The four
sites are static, fast, and carry the catalogue as structured data so a search
engine or an assistant can quote a price correctly. That last part is why every
publishable fact in this repo records where it came from: provenance is a
discriminated union in the type system, and an unconfirmed price renders on the
page but never inside `Offer` schema. A wrong number quoted back as fact
contradicts the booking calendar at the moment somebody is deciding.

**Take the labour out of a photo session.** The studio's real cost is what
happens after the camera stops: staff pick the shots, print them, and hand over
files by whatever method that day allows. The booth does that itself. The
customer pays at the screen, the session runs on a countdown with nobody standing
beside it, they choose a frame and a filter, the sheet prints, and they leave
with a QR that opens their photographs on their phone. The measure of whether it
worked is staff-minutes per session, not uptime.

**Make an outlet cheap to open.** The long-term plan is a franchise: more outlets
running `booth by KAMI` under this brand, sharing one loyalty ledger. That is
deliberately *not* the same as selling photo booth software to other operators —
[Captura](https://captura.id/) already does that, cheaply and with years of head
start. The software exists to make outlets cheap to run, not to be the thing that
is sold. `design/kiosk.md` records the full argument, because it is the decision
that shapes the data model.

## How it fits together

```
   sites/          four static Astro sites on Cloudflare Pages
                   bykami.id · studio. · booth. · dimsamcong.

   api/            one Go binary on a VPS, behind Cloudflare Tunnel
                   app.bykami.id — accounts, loyalty ledger,
                   frame catalogue, operator console
                        ▲
                        │  /v1  — pulls published frames every 5 min
                        │
   agent/          one Go binary on the booth PC in the shop
                   camera → hot folder → SQLite → print queue,
                   serves the kiosk UI on localhost:8899,
                   deletes the photographs after 7 days
```

**Two Go modules, deliberately not one.** They share no code and talk only over
the versioned `/v1` path, because outlet #1 will be running a six-month-old
binary by the time outlet #3 exists, and a shared module would quietly turn a
network boundary into a compile-time one. `go.work` records the argument.

Neither is split into services. On 2 vCPU that would buy three GC heaps and no
scaling — `design/infrastructure.md` has the numbers.

## Running it

Node ≥ 22, pnpm 11.17, Go 1.26. `pnpm install` once at the root.

**The whole photo booth, on a laptop.** No camera, printer, payment account or
booth PC needed:

```bash
pnpm --filter @bykami/kiosk build     # must run BEFORE go build — the agent embeds this
cd agent && go run ./cmd/bykami-agent \
  -root /tmp/booth -source webcam -payments sim -printer sim -sim-print-speed 1000
go run ./cmd/bykami-agent -root /tmp/booth media load 700 "roll 1"
```

Open `http://127.0.0.1:8899`, pick a package, press **Simulasikan pembayaran** on
the QR screen, and the shutter unlocks. Add `?perf=1` to put the client-side
timings on the screen, which is where you want them when you are testing a booth
standing in front of it.

Each simulation is a separate opt-in flag with a startup warning, because none of
them is a product tier — each stands in for hardware or an account that does not
exist yet, and the cost of leaving one on by accident is a booth that gives its
sessions away. The `payment/simulate` route does not exist unless `-payments=sim`
is set: a 404, not a guarded handler, so a booth that could take real money has no
route that skips paying.

**The marketing sites.**

```bash
pnpm --filter @bykami/site-studio dev
```

**The API.**

```bash
cd api && go run ./cmd/bykami -otp-delivery=log -db /tmp/bykami.db
```

**The booking page, against a local API.** Two terminals. No Google account
needed — with no credentials the calendar sync stays off and availability comes
from the database alone, which is also how it behaves on a box nobody has
connected:

```bash
cd api
# Flags come before the subcommand — `flag` stops parsing at the first
# non-flag argument, so `booking seed -db …` would silently use the default.
go run ./cmd/bykami -db /tmp/bykami.db booking seed       # the studio as it trades
go run ./cmd/bykami -db /tmp/bykami.db -otp-delivery=log \
  -admin-phones 081234567890 -booking-origins http://localhost:4321
```

```bash
BYKAMI_API_BASE=http://127.0.0.1:8080 pnpm --filter @bykami/site-studio dev
# then http://localhost:4321/booking, and the operator's day at
# http://127.0.0.1:8080/bookings
```

`-booking-origins` is what lets the dev server's origin through CORS; it is empty
in production, where only the four `*.bykami.id` hosts are allowed.

## Verifying

```bash
pnpm -r typecheck
pnpm -r build && pnpm test    # the SEO contract, asserted against built HTML
pnpm coverage                 # what the catalogue still cannot publish, and why

(cd api   && go vet ./... && go test -race -count=1 ./...)
(cd agent && go vet ./... && go test -race -count=1 ./...)
```

`pnpm test` needs a build first: the contract is asserted against the HTML
actually served, because a component test proves nothing about whether the price
reached the page.

Both Go modules need `-race`. In `api/` the ledger's guarantees *are* concurrency
guarantees — the tests that matter most spawn goroutines to prove a retried
webhook credits once, and that two redemptions cannot both spend the last points.
In `agent/` the print queue drains on one goroutine while the HTTP handler
submits on another.

## CI and deploys

Five workflows. Every third-party action is pinned to a commit SHA — a tag is a
moving pointer, which is how the `tj-actions/changed-files` compromise reached
thousands of repositories at once. Alibaba auth is OIDC, so nothing is stored and
the credentials it buys expire in fifteen minutes; the Pages deploy is the one
job holding a standing API token, and it is skipped for fork pull requests.

| Workflow | On | What it does |
|---|---|---|
| `deploy.yml` | `sites/`, `packages/` | Typecheck, build, SEO contract, then deploy only the sites that changed |
| `api.yml` | `api/` | Vet and test, then publish a release tagged `api-<sha>` |
| `agent.yml` | `agent/`, `apps/kiosk/` | Vet and test, then publish `agent-<sha>` with a Windows and a linux/amd64 binary |
| `ansible.yml` | `ansible/` | `ansible-lint` and `--syntax-check`. Static, holds no key, never reaches the box |
| `alicloud.yml` | `infra/alicloud/` | `terraform validate`, then `plan` against the real account over OIDC |

Both release pollers default to off in their roles. A box that installs whatever
`main` published is a path from a merged commit straight to production, so
turning one on is a deliberate line in a vars file.

The shop's booth PC is the exception, and not by design: it is a Windows machine
with no inbound anything, so its release is installed by whoever is standing at
it.

## Repo layout

| Path | What it is |
|---|---|
| `sites/` | Four Astro marketing sites — `root`, `studio`, `booth`, `dimsamcong` |
| `packages/content` | The catalogue as typed data. Every publishable fact carries its provenance |
| `packages/ui` | Shared layout and headers, including the indexing gate |
| `apps/kiosk` | The booth's React UI. Built into a bundle the agent embeds |
| `api/` | The cloud monolith at `app.bykami.id` — identity, loyalty, frames, operator console |
| `agent/` | The booth binary — capture, compose, print, delivery, retention |
| `ansible/` | What is inside the VPS: base hardening, `cloudflared`, the app, the test booth |
| `infra/cloudflare` | Pages projects, the Zero Trust tunnel, ingress, DNS. State in R2 |
| `infra/alicloud` | The ECS trial box, and the read-only RAM role CI assumes over OIDC |
| `design/` | Decision records. Read these before changing anything structural |
| `scripts/build-linux.sh` | Cross-compiles both deployable binaries into `dist/` |
| `refs/` | The owner's Instagram captures and price-list PDFs. Gitignored |

## Documentation

The design docs are the point of this repo as much as the code is. They record
what was decided and what was rejected, because every one of the rejected options
will look tempting again in six months. `design/kiosk.md` opens by listing an
earlier plan — Electron, Postgres, Redis, MinIO, Kubernetes, multi-tenant SaaS —
and why all of it was dropped.

`STATUS.md` is the honest account of what is live, what is built but gated, and
what is waiting on somebody.

| Doc | What it covers |
|---|---|
| `design/platform-architecture.md` | Vertical map, subdomain strategy, shared identity + loyalty, SEO approach |
| `design/kiosk.md` | The booth: capture, print, delivery, the franchise argument, the competitive read |
| `design/infrastructure.md` | VPS memory budget, Cloudflare Tunnel, Terraform/Ansible split, CI/CD |
| `design/booking-phase2.md` | Self-hosted booking to replace YouCanBook.me, QRIS via Xendit |
| `design/direction.md` | Brand and content read for the studio vertical; verified catalogue and NAP |
| `design/assets-needed.md` | Checklist of assets only the owner can supply |
| `api/README.md` | The HTTP surface, why auth is closed, the operator console, the frame catalogue |
| `agent/README.md` | The booth binary — capture, print, payment gate, download, retention, simulations |
| `agent/internal/compose/templates/README.md` | How a frame template is specified |
| `ansible/README.md` | Every role, the pull-based deploys, backups and the R2 storage cap |
| `infra/cloudflare/README.md` | Running the Terraform stack, and why state moved to R2 |
| `infra/alicloud/ram/README.md` | The CI role's policy, and what adding an apply job requires |

## Reference material, and what is not here

`refs/` holds Instagram captures and the studio's price-list PDFs, all
gitignored. They are inputs to a design process, not assets: the images are the
owner's copyright and this repo is public, and the PDFs are 17 MB of binaries
whose value is the prices, which belong in the pages as crawlable HTML.

This is also why provenance is part of the content type rather than a comment.
Every fact is `verified`, `unverified` or `blocked`: unverified renders but emits
no structured data, and blocked does not render at all. `pnpm coverage` prints
the outstanding set, and CI posts it to every pull request's summary — which
makes "the owner has not confirmed this price yet" a number that shows up on
every build instead of a thing somebody remembers.

The repository carries no licence, so the code is under default copyright and no
permission to reuse it is granted by publishing it here. The brand, the logo and
the photography belong to the studio owner.
