# api — the phase 2 monolith

One Go binary behind Cloudflare Tunnel at `app.bykami.id`. Identity, loyalty and
booking are packages under `internal/`, not services — `design/infrastructure.md`
records why splitting them on 2 vCPU would buy three GC heaps and no scaling.

| Package | Owns |
|---|---|
| `internal/store` | SQLite, pragmas, embedded migrations |
| `internal/phone` | Indonesian mobile numbers → E.164 |
| `internal/identity` | Phone-first accounts, OTP challenges, sessions |
| `internal/loyalty` | The append-only `#SobatKAMi` ledger |
| `internal/httpapi` | JSON transport. Parse, authenticate, delegate, encode |
| `internal/booking` | Availability, bookings, and the calendar sync loop |
| `internal/gcal` | Google Calendar, over `net/http` and no dependency |
| `internal/admin` | The operator console — server-rendered HTML, no JavaScript |

`cmd/bykami` splits the URL space, because it is the only place that knows both
handlers exist: the console takes `/`, the API takes `/healthz` and `/v1/`. Go's
ServeMux prefers the more specific pattern, so the console's catch-all does not
shadow the API — and an unknown path lands on the console rather than a bare
404, which is what a browser is most likely to hit.

`internal/httpapi` holds no business rule on purpose. A rule enforced in a
handler is a rule the next caller reaches around the moment it talks to the
database directly, so the invariants live with the data in the packages above
and this one only chooses status codes.

## The surface

Everything is JSON, and every response carries `Cache-Control: no-store` —
these are auth results and personal data, and Cloudflare sits in front.

| | | |
|---|---|---|
| `GET` | `/healthz` | Readiness. Touches the database |
| `POST` | `/v1/auth/code` | `{"phone"}` → 202. Mints and sends a one-time code |
| `POST` | `/v1/auth/session` | `{"phone","code"}` → `{"token","user"}` |
| `DELETE` | `/v1/auth/session` | Ends this session. Idempotent |
| `GET` | `/v1/me` | The authenticated user |
| `GET` | `/v1/me/loyalty` | `{"balance","entries"}` — balance is `SUM(points)` |
| `GET` | `/v1/booth/frames` | Booth sync: manifest of published, in-season frames |
| `GET` | `/v1/booth/frames/{id}` | The frame's PNG. `ETag`, so an unchanged poll is a 304 |
| `GET` | `/v1/booking/services` | What is on sale. Public, `max-age=300` |
| `GET` | `/v1/booking/availability` | `?service=&from=&to=` → bookable start times, in WIB |
| `POST` | `/v1/booking` | Takes a booking → 201, or 409 if the slot went |
| `GET` | `/v1/booking/{id}` | One booking, for the confirmation page |
| `POST` | `/v1/booking/{id}/cancel` | `{"phone"}` — the number that booked it |

**Booking asks for no login, and is the only surface here that answers a
browser on another origin.** The pages it replaced asked for a name, a number and
an email; putting a session in front of a booking is the surest way to lose one,
and the OTP sender that would issue that session does not exist yet. The number
still becomes an account through `identity.EnsureUser`, so loyalty has something
to attach to later — what is skipped is proving possession of it, which the studio
already lives with because it phones people who do not turn up.

Because `studio.bykami.id` is a different origin from this one, those five routes
carry an `Access-Control-Allow-Origin` allowlist and an `OPTIONS` preflight. Never
a wildcard: two of them write. The `Content-Type: application/json` that `decode`
insists on is doing double duty — it is what makes the preflight happen, and it is
what keeps a cross-origin HTML form out of an endpoint that has no session to
check instead.

**A booth is not a user.** It has no phone and cannot receive a one-time code,
so `/v1/booth/*` authenticates with a shared secret from `BYKAMI_BOOTH_TOKEN`
rather than a session — a different trust level, given a deliberately tiny,
read-only surface. Unset leaves those routes answering 503, which is the same
shape as the auth gate below: a box nobody configured serves no catalogue.

Authentication is `Authorization: Bearer <token>`, never a cookie. Two reasons,
and either alone would decide it:

- The first consumer is the **kiosk at `http://localhost`** on the booth PC. That
  is a different origin from `bykami.id`, so it could not send a platform cookie
  even if one were set — `platform-architecture.md` calls this out as
  "token-based or nothing".
- **`app.bykami.id` is deliberately outside the `.bykami.id` cookie jar.** It is
  the operator-admin surface, and the jar has no opt-out: a `Domain=.bykami.id`
  cookie goes to *every* subdomain including `gallery.bykami.id`, which is the
  highest-risk surface on the platform.

Cookie-borne SSO across the vertical sites is still the plan. It becomes a
decision about which surface sets a cookie over these same tokens, taken per
surface — not a property of this API.

`idempotency_key` is never returned. For a payment-driven earn it is the
gateway's event id, and it is of no use to the customer whose entry it is.

## Auth is closed on the deployed box, and that is the point

`-otp-delivery` is empty by default, and with no delivery configured every auth
route answers **503**. `/healthz` keeps working, so the deploy's health gate and
the tunnel check are unaffected.

This is not a stub waiting to be filled in — it is two standing gates enforced
in code rather than remembered:

- **Residency.** `infrastructure.md` holds the trial box to synthetic sessions
  until the R2-versus-OSS fork is taken. Real logins would create real phone
  numbers on an offshore box.
- **Delivery.** The only sender that exists writes codes to the log, and a
  one-time code in a log file is a one-time code in whatever reads that log.

The gate runs *before* the service, so a request behind it writes no challenge
row — a row that would otherwise still be redeemable once the gate opened.

Sessions already issued are **not** revoked when delivery is switched off.
Changing a delivery setting is a strange way to log everybody out.

Opening it is therefore a deliberate act, and today it is a development one:

```bash
go run ./cmd/bykami -otp-delivery=log -db /tmp/bykami.db
```

The binary logs a warning at startup when that flag is set. The systemd unit in
`ansible/roles/app/templates/bykami.service.j2` does not pass it.

## Verify

```bash
go vet ./...
go test -race -count=1 ./...
```

`-race` because the ledger's guarantees *are* concurrency guarantees: the tests
that matter most spawn goroutines to prove a retried webhook credits once and
two redemptions cannot both spend the last points.

## Deploy

CI builds and publishes a release tagged `api-<sha>`; the box pulls it on a
timer. Nothing reaches into the VPS, because it has one inbound rule and
GitHub's runner ranges are far too broad to allowlist without undoing the reason
the tunnel exists. See `ansible/README.md`.

From a workstation, pushing rather than waiting for a pull:

```bash
./scripts/build-linux.sh                   # leaves dist/bykami-linux
cd ansible && ansible-playbook site.yml --tags app -e @booth.vars.yml
```

`copy` compares checksums, so redeploying an unchanged binary fires no handler
and does not restart the service. A second identical run reports `changed=0`,
which is what makes it safe to run whenever you are unsure.

## The operator console

`/` is the console, not an API route. Server-rendered HTML, no JavaScript, no
build step — the alternative is a second toolchain and a client-side session to
keep correct so that staff can look up a phone number.

| | |
|---|---|
| `GET /` | Login, or a redirect to the console when already signed in |
| `POST /login` `POST /verify` | The same OTP flow customers use |
| `GET /customers?phone=` | Balance and ledger history for one customer |
| `POST /customers/{id}/adjust` | Writes a compensating entry |
| `GET /frames` | The frame catalogue, with detected slots drawn over each one |
| `POST /frames` | Upload a PNG. Everything else is read out of the file |
| `POST /frames/{id}/publish` | Put it on the booths, or take it off |
| `POST /frames/{id}/season` | Set or clear the date window |
| `POST /frames/{id}/delete` | Remove it |

**A frame is a PNG and nothing else is typed in.** The sheet size comes from its
dimensions and the photo cells from its transparent regions — flood-filled, then
filtered by size so a decorative cut-out does not become a slot a customer is
asked to fill with their face. A rectangle typed next to a picture that already
contains it is a chance to disagree with the picture, and the symptom is a face
printed off its slot, found on paper.

A hole does not have to be a rectangle. The photo fills the hole's bounding box
and the artwork, drawn over it, masks the photo back to whatever shape was cut,
so round and heart-shaped slots print correctly. What is rejected is a region
nowhere near its own bounding box — a border with a transparent middle, which
would otherwise be read as one slot the size of the sheet.

Uploads land **unpublished**. Detection is inference, and the check on inference
is a person looking at the slots drawn over the frame; publishing on upload
would put that check after the customer. The preview is checkered, so a hole
filled with white — the usual export mistake — reads as artwork rather than as a
hole.

### The catalogue is also a subcommand

The console is the tool for this, but its login is the same OTP flow customers
use — and on a box with no delivery configured, which is the deployed state,
nobody can sign in at all. So the catalogue is reachable from a shell too, the
same way `bykami-agent media` is:

```bash
bykami -db /var/lib/bykami/bykami.db frames list
bykami -db … frames import strip-4.png "Klasik Empat" klasik
bykami -db … frames publish klasik-empat
bykami -db … frames season ramadan-2027 2027-02-08 2027-03-09
```

`import` prints the detected slots so they can be checked, and leaves the frame
unpublished — the same rule the console follows, for the same reason.

This is preferable to switching the development OTP sender on to get a login: a
one-time code in a log file is a one-time code in whatever reads that log.

**A booth's frames are the built-ins *plus* whatever is published here.**
`agent/cmd/bykami-agent` appends the synced set onto `compose.Builtin()` rather
than choosing between them — a catalogue frame only displaces a built-in when it
carries the same id. So shipping a binary with new house frames does not
withdraw anything from the catalogue, and a request to "replace all the frames"
is two jobs: the release, and then

```bash
bykami -db … frames unpublish <id>     # booths drop it within five minutes
```

This is worth stating because it fails quietly in the direction of more. Nobody
notices the extra designs in a test — the booth still works, every frame still
prints — and the tell arrives as a customer picking artwork that was supposed to
be gone. Unpublish is the reversible half of the pair; `delete` is for artwork
that was wrong rather than artwork that is finished.

**Seasons are dates, not a switch somebody flips.** A Ramadan frame that has to
be turned off by hand is one that is still on the booth in August, and the
person who notices is a customer. The form asks for the last day it runs,
because that is how a person thinks about a season; the catalogue stores the
instant it stops.

Artwork is a `BLOB`. A full catalogue is single-digit megabytes — smaller than
the ledger will be within a year — and object storage would add a bucket, a
credential to rotate and a second thing that can be down while the database is
up. In the database the bytes share a backup and a transaction with the row
describing them, so a restore cannot produce a frame with no picture.

**Who is an operator is configuration, not data.** `-admin-phones` is a
comma-separated allow-list, checked against the *currently verified* session on
every request. There is no role column, deliberately: a role in the database has
a bootstrap problem — the first operator must be promoted by an operator — whose
usual answer is a seed script that quietly becomes a way to grant admin. It also
means revoking someone takes effect on their next request rather than whenever
their session happens to expire, and that a stolen customer session cannot
become an operator session, because privilege is never stored in the session.

Empty means nobody, which is the deployed default. This is a public hostname.

**This surface gets a cookie although the API does not.** A browser can send one
and an HTML form has nowhere to keep a bearer token without the JavaScript this
package exists to avoid. It is `__Host-` prefixed — a name browsers refuse
unless the cookie is `Secure`, has `Path=/`, and carries **no `Domain`** — which
turns the host-only rule from something this code must remember into something
the browser will not let it break.

State-changing forms carry a CSRF token derived from the session token itself,
so nothing has to be stored or expired beside it. A cross-site form cannot
compute it, because the cookie it derives from is `HttpOnly`, host-only and
`SameSite=Strict`.

Adjust is the only mutation, and it is the only one the ledger permits: history
is corrected by writing a compensating entry, never by editing a row. The
operator's number is recorded in the entry's reason, because the ledger has no
actor column and an anonymous adjustment cannot be defended later.

## Not here yet

- **Earn and burn have no HTTP route.** Earning is a machine action from the
  kiosk and needs a credential that is neither a customer's session token nor an
  operator's — that is the open staff/device auth question in `design/kiosk.md`.
- **Nobody can actually sign in to the console** on the deployed box, because
  login uses the OTP flow and delivery is off. That is the gate above, working.
- **A real OTP sender.** WhatsApp is intended and needs a provider account. It
  is the single thing blocking the console from being usable.
- **No per-booth identity.** One shared secret admits every booth, so a single
  booth cannot be revoked without rotating all of them. Worth fixing when there
  is a second outlet, not before.
