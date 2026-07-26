# Phase 2 — self-hosted booking + QRIS

Decision record. Captures what was settled, what is built, and what still blocks
design. Phase 1 shipped first and is live on `bykami.id`.

## Built — `api/`

The architecture record requires booking to be built against shared identity
from day one, because a vertical with its own user table means a migration and
forcing every customer to re-register when loyalty launches. Identity and
loyalty are also the two parts of phase 2 that no external dependency blocks, so
they went first:

- **`internal/phone`** — Indonesian mobile numbers normalised to E.164. Load
  bearing rather than cosmetic: the number *is* the account, so two spellings
  are two balances, and merging them later means rewriting an append-only ledger.
- **`internal/identity`** — phone-first OTP, no passwords. Codes are stored as
  hashes and are single-use, attempts are counted before they are checked, and
  requests are rate limited per number. Sessions are scoped for `.bykami.id`.
  Delivery sits behind a `Sender` interface, which is what let this be finished
  and proven before a WhatsApp provider account exists.
- **`internal/loyalty`** — the append-only ledger. Balance is `SUM(points)`,
  never a stored column.
- **`internal/store`** — SQLite with WAL, foreign keys on, embedded migrations.

The ledger's guarantees are enforced by the schema, not by the callers, on the
same principle already recorded for double-booking — application-level checks
lose to concurrent requests:

| Guarantee | Mechanism |
|---|---|
| A retried webhook credits once | partial `UNIQUE` index on `idempotency_key` |
| An `earn` cannot subtract | `CHECK` tying sign to kind |
| An `earn` cannot skip its key | `CHECK (kind <> 'earn' OR idempotency_key IS NOT NULL)` |
| History cannot be rewritten | `BEFORE UPDATE` / `BEFORE DELETE` triggers that `RAISE(ABORT)` |
| A customer with history cannot vanish | foreign key with `NO ACTION`, not `CASCADE` |

Corrections are made with a compensating `adjust` entry, which is the only route
the triggers leave open — so the mistake and its fix are both auditable months
later, which is what makes a dispute resolvable.

Tested under `-race`, including the two cases that only fail in production:
sixteen concurrent earns with one idempotency key credit once, and eight
concurrent redemptions of the last 100 points yield exactly one success.

## Still blocked

Unchanged by the above, and neither is a code problem:

- **Bookable resource count** blocks the availability engine entirely.
- **Xendit merchant onboarding** blocks QRIS. Long pole; start it early.

## Decisions

**Sequencing — landing page first, booking second.**
The page delivers the SEO/LLM goal on its own and is not blocked by payment
gateway onboarding, which takes weeks regardless. Booking links point at the
existing YouCanBook.me calendars until this lands, then swap. Nothing is wasted.

**Payment model — optional prepay, `BOOKING TANPA DP` preserved.**
Customer chooses: pay by QRIS at booking time, or pay at the studio as today.
The tagline stays true and no marketing has to be rewritten.

**Gateway — Xendit.**
Cleaner API and better docs for a custom Go integration. QRIS MDR is set by Bank
Indonesia at 0.7% and is identical across gateways, so pricing was not a factor.
Micro-merchant category may qualify for lower — confirm during onboarding.

## Why optional prepay matters technically

It removes the riskiest subsystem outright.

With **required** payment, a slot must be held while the customer scans and pays,
then released if QRIS expires. That means a state machine racing a webhook
against a timeout, with duplicate-delivery and reconciliation edge cases. It is
where most self-built booking systems carry their worst bugs.

With **optional** payment, a booking is confirmed the instant it is made. Payment
is a separate transaction attached to an already-confirmed booking, and can even
be completed later via a sent QRIS link. No hold, no release, no race.

What remains genuinely hard is **double-booking prevention**: a unique constraint
on `(resource_id, start_time)` enforced at the database, not in application code.
Application-level checks lose to concurrent requests.

## Blocking — needed before design can start

**How many bookable resources are there?**
Self-photo rooms? Photobox booths? Is the photographer a resource that can be
booked in parallel with a self-photo session, or does one person run everything?

The availability engine's entire design depends on this. Two separate
YouCanBook.me calendars implies at least two independent resources, but that must
be confirmed rather than inferred. Without it, slot allocation cannot be modelled.

**Merchant onboarding** — business entity, NPWP, bank account in the business
name, and Xendit KYC. Days to weeks, entirely outside the build. Start it early;
it is the long pole.

## Rough scope, once unblocked

- Availability engine — hours 09:00–21:00, per-service durations
  (10/15/20/25/40 min), buffers, blackout dates, per-resource calendars
- Booking CRUD with DB-enforced uniqueness per resource-slot
- Optional QRIS charge via Xendit + idempotent webhook handling
- Confirmation and reminders — email and WhatsApp
- Reschedule and cancellation
- Admin — calendar view, block time, walk-in entry, refunds

## Open questions

- Does the studio currently rely on YouCanBook.me's Google Calendar sync? If so,
  that has to be replicated or deliberately dropped.
- Refund policy — needed before building a refund flow
- Who staffs the admin panel, and on what device? Likely mobile-first.
- Retain booking history from YouCanBook.me, or start clean?
