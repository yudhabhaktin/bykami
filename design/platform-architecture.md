# bykami — platform architecture

Decision record. bykami is a multi-vertical local platform for Banyuwangi, not a
single business with a website. Architecture must accommodate verticals that do
not exist yet without rebuilding the ones that do.

## Verticals

| Vertical | Status | Domain |
|---|---|---|
| studio by KAMI — self photo, pas foto | Operating | `studio.bykami.id` |
| booth by KAMI — mobile photobooth for events | Operating | `booth.bykami.id` |
| Dimsamcong — F&B | Operating | `dimsamcong.bykami.id` |
| photo by KAMI — on-location, video | Operating | Deferred — no catalogue gathered |
| Further verticals | Undefined | — |

Booth was promoted out of studio into its own vertical: it has its own Instagram
account and its own price list, it serves Banyuwangi, Jember, and Bondowoso, and
it travels to the customer rather than operating from the Jajag address. Those
are different customers asking different questions, which makes it a different
site rather than a section.

Photo is deferred, not dropped. Unlike studio and booth it has no price-list PDF
to build a catalogue from, so a page for it would be an empty shell.

Platform root `bykami.id` — brand, vertical directory, account and loyalty portal.

**Design for extensibility, not for specific unknowns.** A new vertical should
need only its own catalog and fulfilment logic; identity, loyalty, payments, and
notifications come from the platform.

## Why subdomains work here

Chosen deliberately. Two consequences, one good and one to manage.

**Good — SSO is nearly free.** A session cookie scoped to `.bykami.id` is
readable by every subdomain beneath it. One login works across studio, photo, and
food with no cross-domain OAuth. Separate domains (`dimsamcong.com`) would have
forced a redirect dance for every property.

**To manage — link authority does not flow between subdomains.** Each builds its
own from zero. Mitigations below.

## SEO strategy

Local search, not domain structure, drives conversion here. Ranking for
*"self photo studio banyuwangi"* is won in Maps and Google Business Profile.

- **One Google Business Profile per physical location.** The studio in Jajag and
  any F&B outlet are separate businesses to Google regardless of URL structure.
  This outweighs everything else for a local operator.
- **One `Organization` entity in schema.** Every subdomain declares
  `parentOrganization` → `bykami.id`. Entity consolidation is independent of link
  authority; engines will understand it is one company.
- **Sitemap index at the root** referencing each subdomain's sitemap.
- **Cross-link in nav and footer** across all properties — internal links carry
  signal.
- **No duplicate content** across subdomains. Shared NAP is fine; shared copy is not.
- **Per-vertical backlink effort.** Local directories, press, partner links —
  each subdomain earns its own.
- Prices as crawlable HTML with `Offer` schema on every vertical. This is the
  original problem and it applies to F&B menus exactly as it does to photo packages.

## Shared platform services

Owned centrally. Verticals consume, never reimplement.

### Identity

Phone-first, matching Indonesian norms — **not** email-first.

- Primary identifier: phone number, verified by WhatsApp or SMS OTP
- **No passwords.** OTP only. Fewer support requests, no credential-stuffing
  surface, and it matches what this market already expects.
- Optional: name, email
- Session cookie scoped to `.bykami.id` for cross-vertical SSO
- Minimal PII by default — collect only what a booking or order actually needs

### Loyalty — `#SobatKAMi`

The Instagram community framing (`#SobatKAMi`, `Story Kamu`) is already a loyalty
brand. Local competitors run "royalti card" schemes, so the market expects this.

**The critical design rule: an append-only ledger, never a mutable balance column.**

```
loyalty_entry
  id, user_id, vertical, kind ('earn'|'burn'|'adjust'),
  points (signed), reference_id, idempotency_key, created_at
```

Balance is `SUM(points)` for a user. Never store and mutate a running total.

Why this matters: mutable balances drift, cannot be audited, and produce disputes
you cannot resolve. An append-only ledger means every point is traceable to the
event that created it, and a bug is correctable with a compensating entry rather
than a manual balance edit.

`idempotency_key` is mandatory on earn events. A retried payment webhook must not
credit twice.

Cross-vertical by design: earn on a photo session, spend on dimsum.

**Business rules still undecided** — earn rate, redemption value, expiry, tiering.
These are the owner's calls, not architectural ones.

### Payments

Xendit, QRIS-first. See `booking-phase2.md`. Shared across verticals so a single
merchant account and one reconciliation flow serves all of them.

### Notifications

WhatsApp primary, email secondary. Booking confirmations, order status, loyalty
milestones. One service, templated per vertical.

## Vertical-owned

Each vertical owns only:

- Its catalog (packages, menu)
- Its fulfilment model (slot booking vs order queue)
- Its own pages and content

## Consequence for phase 2

**The booking system must be built against shared identity from day one.**

Shipping studio booking with its own isolated user table means a data migration
and forcing every existing customer to re-register when loyalty launches. The
identity and loyalty data model is therefore designed *before* booking is built,
even though both ship together.

## Phasing

1. **Phase 1 — marketing pages.** Static, SEO-first, booking links point at
   YouCanBook.me. Ships independently, unblocked by anything below.
2. **Phase 2 — identity + loyalty + booking + QRIS.** Built together against the
   shared model. Gated on Xendit merchant onboarding.
3. **Phase 3 — F&B vertical.** Reuses identity, loyalty, payments, notifications.
   Adds menu and ordering only.

## Open

- ~~Domain for the F&B vertical~~ — settled as `dimsamcong.bykami.id`. It keeps
  the name customers already know from Instagram, rather than filing it under a
  generic `food.` that nobody searches for.
- Loyalty earn and redemption rules
- Whether verticals share one legal entity — affects the Xendit merchant setup
  and whether one account can settle for all of them
- Bookable resource count at the studio (blocks booking design — see
  `booking-phase2.md`)
