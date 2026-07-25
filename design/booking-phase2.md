# Phase 2 — self-hosted booking + QRIS

Decision record. Not yet a plan; captures what was settled and what still blocks
design. Phase 1 (the landing page) ships before any of this.

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
