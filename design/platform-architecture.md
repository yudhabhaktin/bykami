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

Non-vertical surfaces, which serve every vertical rather than belonging to one:

| Surface | Domain | Notes |
|---|---|---|
| Operator admin | `app.bykami.id` | Host-only cookie — deliberately *not* in the `.bykami.id` jar |
| Customer gallery | `gallery.bykami.id` | Static HTML, no JS, no cookies. See `kiosk.md` |
| Kiosk UI | `http://localhost` on the booth PC | Served by the agent binary; a different origin from everything above |

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

**To manage — the jar has no opt-out.** A `Domain=.bykami.id` cookie is sent to
*every* subdomain; there is no way to scope it to "all except one". So any
surface placed under `bykami.id` must be one where stealing that cookie is
impossible, or must not receive it. Two consequences already taken:

- `app.bykami.id` (operator admin) sets a **host-only** cookie — no `Domain`
  attribute — so the admin session never enters the jar and never leaks to a
  vertical.
- `gallery.bykami.id` is the highest-risk surface on the platform: public URLs,
  shared into WhatsApp groups, containing customers' faces. It is held to static
  HTML with no JavaScript and a strict CSP, guarded by a contract test, so that
  there is no script context in which the platform cookie could be read.

A separate registrable domain would have given this for free via origin
isolation. The subdomain was chosen for brand and for free DNS in the existing
Terraform-managed zone; the mitigations above are the price.

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
- Session cookie scoped to `.bykami.id` for cross-vertical SSO — but see the
  jar caveat above; not every surface gets it
- Minimal PII by default — collect only what a booking or order actually needs

**The kiosk cannot use the session cookie at all.** It runs at `localhost`, a
different origin, so it is token-based or nothing. It captures a phone number to
unlock digital files, stores it *unverified*, and credits loyalty only once that
number is verified through the OTP flow. Unverified numbers never earn, which is
what keeps the ledger clean given the number *is* the account.

### Loyalty — `#SobatKAMi`

The Instagram community framing (`#SobatKAMi`, `Story Kamu`) is already a loyalty
brand. Local competitors run "royalti card" schemes, so the market expects this.

**The critical design rule: an append-only ledger, never a mutable balance column.**

```
loyalty_entry
  id, user_id, vertical, outlet_id, kind ('earn'|'burn'|'adjust'),
  points (signed), reference_id, idempotency_key, created_at
```

`outlet_id` exists because step two is a **franchise**, not software sold to
other operators. Franchise outlets run `booth by KAMI` under this brand and this
loyalty program, so the ledger stays **pooled** — a guest earns in Jember and
redeems at Dimsamcong in Banyuwangi, and every outlet added makes membership
worth more. `outlet_id` is for attribution and settlement, not isolation.

Add it now, before there is data to migrate. Deliberately *not* added:
`tenant_id` and per-tenant isolation, which would be required only if the
customer were a competing operator — see `kiosk.md`.

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
3. **Phase 3 — kiosk.** Self-service capture, print and delivery at the studio.
   Runs in parallel with phase 2: it consumes identity and loyalty but is the
   only workstream blocked on nothing external. See `kiosk.md`.
4. **Phase 4 — F&B vertical.** Reuses identity, loyalty, payments, notifications.
   Adds menu and ordering only.

## Open

- ~~Domain for the F&B vertical~~ — settled as `dimsamcong.bykami.id`. It keeps
  the name customers already know from Instagram, rather than filing it under a
  generic `food.` that nobody searches for.
- Loyalty earn and redemption rules
- Whether verticals share one legal entity — affects the Xendit merchant setup
  and whether one account can settle for all of them
- Franchise terms — fee, revenue share, and whether a franchisee's sessions earn
  into the pooled ledger at the same rate as an owned outlet
- Bookable resource count at the studio (blocks booking design — see
  `booking-phase2.md`)
