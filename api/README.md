# api — the phase 2 monolith

One Go binary behind Cloudflare Tunnel at `app.bykami.id`. Identity, loyalty and
(later) booking are packages under `internal/`, not services — `design/infrastructure.md`
records why splitting them on 2 vCPU would buy three GC heaps and no scaling.

| Package | Owns |
|---|---|
| `internal/store` | SQLite, pragmas, embedded migrations |
| `internal/phone` | Indonesian mobile numbers → E.164 |
| `internal/identity` | Phone-first accounts, OTP challenges, sessions |
| `internal/loyalty` | The append-only `#SobatKAMi` ledger |
| `internal/httpapi` | Transport. Parse, authenticate, delegate, encode |

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
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o /tmp/bykami ./cmd/bykami
cd ../ansible && ansible-playbook site.yml --tags app -e app_binary_src=/tmp/bykami
```

`copy` compares checksums, so redeploying an unchanged binary fires no handler
and does not restart the service. A second identical run reports `changed=0`,
which is what makes it safe to run whenever you are unsure.

## Not here yet

- **Earn, burn and adjust have no HTTP route.** The ledger supports all three.
  Earning is a machine action from the kiosk and adjusting is an operator
  action, and both need a credential that is not a customer's session token.
  Staff auth is an open question in `design/kiosk.md`; inventing one to fill
  this table would be the throwaway admin auth `direction.md` already refused.
- **No operator admin UI**, for the same reason.
- **Booking**, blocked on the bookable-resource count.
- **A real OTP sender.** WhatsApp is intended and needs a provider account.
