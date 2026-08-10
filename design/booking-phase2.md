# Phase 2 — self-hosted booking + QRIS

Decision record. Captures what was settled, what is built, and what still blocks
design. Phase 1 shipped first and is live on `bykami.id`.

**Booking is built.** It is on `studio.bykami.id/booking`, against
`internal/booking` and `internal/gcal`, and the resource question that blocked the
availability engine was answered by reading the two YouCanBook.me calendars
through their own JSON API on 2026-08-10 — see "What the old calendars turned out
to say" below. QRIS is still blocked on Xendit onboarding, and booking does not
wait for it: the studio takes no deposit today, so a confirmed booking with no
payment attached is the existing arrangement rather than a compromise.

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

## What the old calendars turned out to say

Both booking pages were read through `api.youcanbook.me` rather than screen
scraped, which is why the numbers below are exact.

**Three resources, not one, and not six.** The two pages presented six choices —
Y2K, Vintage, Maroon, Self Photo Basic, Self Photo MOTIF, Pas Photo — while
serving one shared availability pool: both calendars returned a byte-identical
slot set, including one-off gaps, so a photobox booking blocked a self-photo
session that could physically have run alongside it. Two separate Google Maps pins
were configured, one per page, which is the strongest evidence in any source that
they are separate rooms. The owner confirmed photobox and self-photo run in
parallel, so `booking_resources` is seeded with three — photobox, self-photo, and
the off-site photographer — and the new system therefore offers *more* capacity
than the one it replaced. Whether Pas Photo can truly run alongside self-photo is
still open; both need the operator, and it is a row to move if not.

**Resources are rows, not code.** The count has already been wrong once, which is
the argument against a constant: adding a room is an `INSERT`.

**09:00–21:00 every day, on a 30-minute grid, last start 20:30.** Two standing
breaks, derived from 31 days across both calendars rather than asked for: 17:30
blocked every single day, and 12:00 blocked every day except Friday, when the
midday break sits at 11:30 for Jumatan. Minimum notice ~30 minutes; window 31
days. All of it is in `booking_hours` and `booking_breaks`, because these are the
first thing Ramadan changes and the alternative is a deploy to move a prayer
break.

**A cancellation policy nobody had recorded.** The form made a customer tick a box
agreeing to it: *"Terlambat, cancel dan reschedule kurang dari H-6 jam dikenakan
denda sebesar 20k"*, plus liability for damaged property. It is now in
`packages/content` with the calendar as its source, which answers the "no
reschedule or cancellation policy exists in any source" gap. It is **not
enforced** — see the open questions.

**A whole service line the repo had never recorded.** Self photo on a patterned
backdrop, at 80K/120K/180K, absent from every price-list PDF.

**Headcount bands read as bands, not cumulatively.** The PDF's "MIDI 1-4 ORANG" is
"3-4 ORANG" on the booking page, which is how the package is actually priced — a
pair booking MIDI would be paying MINI's price for MINI's room.

**MINI's duration is the one place the calendar loses.** It sold fifteen minutes;
the owner said five in August 2026 and the booth counts five down on the capture
screen. The booking page and the PDF agree on fifteen precisely because the page
was configured from the PDF, which makes them one stale source rather than two
agreeing, so five is what is seeded. Availability is unaffected either way — both
round to one half-hour slot.

## Google Calendar — replicated, with a service account

`booking-phase2.md` asked whether the studio relied on YouCanBook.me's Google
Calendar sync and whether that had to be replicated or deliberately dropped. It is
replicated: the owner works out of that calendar and blocks their own time in it,
so `internal/gcal` reads `freeBusy` into a cache and writes every booking back as
an event.

**A service account, not OAuth**, and the reason is operational. An unverified
Google Cloud project issues refresh tokens that expire after seven days, and the
Calendar scope is one Google treats as sensitive — so a consent-screen integration
works for a week and then stops silently, weeks before anybody connects the two
facts. A consumer Gmail account can share a calendar with a service account's
address the same way it would with a colleague; the key does not rotate, and the
one manual step is that share, set to "Make changes to events".

**No new dependency.** `google.golang.org/api` would be the largest thing in the
module by an order of magnitude, and what is actually needed is a signed JWT, a
token exchange and three REST calls, all of which the standard library does.

**The busy cache is a cache, and that is load bearing.** A failed poll leaves the
previous ranges in service and records the error for the console. A studio that
stopped selling because an API in Mountain View was slow would have traded a real
sale for a hypothetical conflict — the same rule `internal/framesync` follows on
the booth. The event write is best-effort with a retry queue for the mirror image:
a booking the customer has been told is confirmed must never be lost because
Google returned a 500.

## Still blocked

- **Xendit merchant onboarding** blocks QRIS. Long pole; start it early. Booking
  does not wait for it.

## Decisions

**Sequencing — landing page first, booking second.**
The page delivers the SEO/LLM goal on its own and is not blocked by payment
gateway onboarding, which takes weeks regardless. Booking links point at the
existing YouCanBook.me calendars until this lands, then swap. Nothing is wasted.

*Settled as written.* The swap is one line — `studio.nap.bookingUrl` becomes
`/booking` — and it is deliberately not made yet: the page is reachable by URL and
linked from nowhere until the calendars are connected and a real booking has been
watched arriving in Google Calendar. `StickyBar` already renders the second button
the moment that value stops being `blocked`.

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

**Merchant onboarding** — business entity, NPWP, bank account in the business
name, and Xendit KYC. Days to weeks, entirely outside the build. Start it early;
it is the long pole.

## Scope, and what of it is built

- [x] Availability engine — hours 09:00–21:00, per-service durations
      (5/10/15/20/25/40/60/90/180 min), buffers, recurring prayer breaks,
      blackout dates, per-resource Google calendars
- [x] Booking CRUD with DB-enforced uniqueness per resource-slot. `booking_slots`
      holds one row per half hour a session occupies, keyed
      `(resource_id, starts_at)` — a unique index on the *start* instant would
      accept a three-hour shoot at 09:00 and another at 10:00
- [x] Google Calendar both ways: `freeBusy` in, events out
- [x] Cancellation, by the customer with their own number or by an operator
- [x] Admin — day view, block time, cancel, calendar health
- [ ] Optional QRIS charge via Xendit + idempotent webhook handling — blocked on
      onboarding
- [ ] Confirmation and reminders — email and WhatsApp. Both wait on the same
      provider account the OTP sender waits on; the booking page's own
      confirmation screen and an add-to-calendar link stand in for now
- [ ] Reschedule. Cancel-and-rebook works today, which is two steps for the
      customer and loses the original booking's history
- [ ] Walk-in entry, so a session sold at the counter shows in the same day view
- [ ] Refunds — needs a refund policy first

## Open questions

- **Is MINI five minutes or fifteen?** The owner and the booth say five; the
  calendar the studio was selling on said fifteen. Five is what ships.
- **Can Pas Photo run alongside self-photo?** Both need the operator. It is seeded
  on the self-photo resource, which is the conservative reading.
- **Travel time for off-site work.** Photographer and videographer sessions run
  one to three hours at the customer's location and are seeded with no buffer,
  because a guessed one would be a wrong number in the schema. Two shoots an hour
  apart across town are currently both bookable.
- **Should an off-site shoot instant-confirm?** It is priced per hour and needs a
  person to travel, so "request, then accept" may fit it better than the
  instant confirmation the in-studio packages get.
- **How is the 20K late/cancellation charge collected?** The policy is published
  and agreed at booking; nothing enforces it, and nothing should until somebody
  decides how the money is taken. A system that refused a late cancellation would
  produce a no-show instead of a freed slot.
- Refund policy — needed before building a refund flow
- Who staffs the admin panel, and on what device? Likely mobile-first.
- Retain booking history from YouCanBook.me, or start clean?
- **`0811-3777-10`** on the price list is two digits short of an Indonesian mobile
  number, and it is the WhatsApp number all four sites publish.
