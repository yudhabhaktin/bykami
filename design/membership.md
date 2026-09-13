# Membership — the paper card, as a view over the ledger

The studio sells a physical loyalty card. A customer buys Rp 45,000 and gets a
stamp; nine stamps fill the card, and five of those slots carry a gift. Staff
tick it with a pen, and it is keyed on a name and a WhatsApp number written on
the front.

This record is about replacing it. What is decided here is not a new loyalty
system — `internal/loyalty` is already that, and `design/platform-architecture.md`
already argues why there is one ledger and not one per vertical. What is
decided is how the paper card maps onto it, and what has to be added around it
for the replacement to be credible: a card that can fill and close, a gift that
cannot be paid out twice, an operator surface that is faster than a pen, and a
customer surface that needs no provider account.

## What the card says

| Slot | Gift |
|---|---|
| 2 | +5 menit |
| 4 | 1 print 4R |
| 6 | +5 menit |
| 8 | 1 print 4R |
| 9 | Free photobox 1 orang |

- One stamp per **multiple of Rp 45,000** — `kelipatan`, so Rp 90,000 is two
- The remainder earns nothing: Rp 44,000 twice is zero stamps, not one
- A gift earned today is redeemed on a later visit, never the same day
- Nine stamps fill the card

The last two are the whole design. Everything below is a consequence of them.

## The decision: a stamp is the studio's unit in the existing ledger

**A stamp is not points and it is not rupiah.** It is a unit with no cash value,
spendable on nothing except the ladder, and it is counted **per transaction**
because the card floors per transaction.

The tempting alternative was to keep the ledger in rupiah-denominated points —
one point per Rp 1,000, a stamp at 45 points — and let the stamp count fall out
of the balance. It is rejected because it silently changes the product: under
that rule Rp 44,000 spent twice earns a stamp, and the customer holding a paper
card with nothing on it after two visits is being told the rules changed. The
card's rule is a floor, a floor is per transaction, and a balance cannot express
one. So the studio's earn is `Earn(user, "studio", floor(amount / 45,000), …)`
and the unit is the stamp.

The second alternative — a `stamps` column on the user, incremented — is the
mutable counter this ledger exists to forbid. It is not a close call.

Two consequences fall out, and both are deliberate:

- **The remainder is dropped.** Not a rounding error to fix later; the card
  advertises it. A carry-over would be a friendlier product and a different one,
  and it is a business decision rather than a bug — say so in the release note
  rather than quietly implementing it.
- **The card groups stamps, and a group is a row.** `SUM(points)` says how many
  stamps somebody has ever earned; it cannot say which of them are on the card in
  their hand, and therefore cannot say when a card fills. Hence
  `membership_cards`.

## A gift is a row, not a balance

`+5 menit` and `Free photobox 1 orang` are fulfilments, not credits. Model them
as points and two staff members looking at the same phone number can each spend
the same gift, because both read a balance of one and neither write contradicts
the other.

So a gift is issued as a row with a state, and the transition is a conditional
`UPDATE` whose row count is the answer:

```sql
UPDATE membership_rewards
   SET redeemed_at = ?, redeemed_by = ?
 WHERE id = ? AND redeemed_at IS NULL AND redeem_after <= ?
```

One row changed means it happened. Zero means it was already taken or too early
— and the message the operator sees distinguishes them. This is the
same shape as `booking_slots`' primary key: Go produces the error message, the
database has the only vote.

**The same-day rule is the database's too.** `redeem_after` is the WIB date the
reward was issued, plus one day, stored as an epoch second. It is not a check in
a handler, because it is the card's printed fine print and not a screen's
opinion.

## Decisions taken

**The card closes at nine and the next purchase opens a new one.** A partial
unique index enforces at most one open card per member per program, which is what
makes "which card does this stamp belong to" unambiguous. Stamps never migrate
between cards.

**Stamps never expire, and an unredeemed gift survives the card closing.** Both
are the friendlier reading of a card that says nothing about expiry, and both are
easier to tighten later than to loosen: adding an expiry to rows that were
written under a no-expiry promise is a change customers can feel.

**Already-earned gifts are not reissued when paper cards are imported.**
Somebody bringing in a card with eight ticks has collected the gifts for 2, 4, 6
and 8 on paper. The import mints them and immediately marks them redeemed, naming
the paper card. Without that step, every member with a nearly-full card collects
four free gifts in the same week, and it is the kind of mistake that is
invisible until it is expensive.

**Stamps may be minted by staff, and that is a deliberate departure from "an
unverified number never earns".** `design/platform-architecture.md` states the
rule, and it was written about the kiosk: a booth captures a number with nobody
checking it, so the guard against a typo, a joke, or somebody else's number is
that nothing credits until that number completes the OTP flow. The counter is
the opposite case. A staff member is looking at the customer — and at the phone
in their hand — when they write the stamp, so provenance is a human, and the
rule that matters is that the *entry* says which human. `membership_purchases`
carries the operator's number for exactly this reason, and the ledger entry's
reference names the purchase.

Without this read, nothing can earn until a WhatsApp provider exists, which means
the paper card stays the real system indefinitely. That is the whole point of the
change.

## The customer reaches the card by phone number

`POST /v1/membership/lookup` takes a WhatsApp number and returns the card. No
session, no cookie, no code, no provider.

**The rejected design was a bearer link**: an unguessable per-member token, QR
code, the same trade `agent/`'s delivery page makes. It is rejected here because
the card is *printed* — a token on paper is a secret that ends up photographed,
sticky-noted to a monitor, and passed around, and a lost one has to be reissued
through the console by hand. A phone number is already written on the front of
the card and is already the account, so it is the key that needs no distribution.

**What that costs, stated plainly:** anybody who types a number learns whether it
is a member and how many stamps they hold, so a list of numbers can be walked
through. The mitigations are honest rather than clever:

- The **name** comes back as a first name and an initial — `Isyara H.` — because
  the name is the thing the caller could not supply, and therefore the thing an
  enumeration would harvest. The number is echoed in full: the caller typed it,
  so masking it would hide it from the only person who already knows it.
- Rate-limited per phone **and** per IP, by rows in `membership_lookups`, swept
  as they age. The same shape as `identity`'s existing challenge limit, so there
  is one idea in the codebase about what a rate limit is.
- **Nothing on that page can take a gift.** Redemption is an operator action in
  the console, because a gift is a fulfilment somebody has to hand over. The
  worst case of a successful enumeration is knowing that a number has seven
  stamps.

If the owner later wants it tighter, the upgrade is a four-digit PIN set at the
counter, stored the way an OTP code is — a hash — and still no provider. Nothing
above forecloses it.

## The operator surface

`/stamps` in the console: type a WhatsApp number, see the member and the card,
type an amount, confirm. Unknown number → the same screen asks for a name and
creates the member, which is how somebody joins — no OTP, no account form, no
app.

- The stamps an amount earns are shown **before** it is written
  (*"Rp 88.500 = 1 stempel"*), because the amount is typed by hand from a receipt
  and 88.500 against 885.000 is a factor of ten.
- The receipt or order reference is the idempotency key. A double-tap writes one
  set of stamps and the second attempt reports the first one.
- A below-threshold amount is refused with the shortfall, not recorded as a
  zero-point row.
- Everything the operator wrote carries their number.

Below it, the outstanding gifts with a redeem button, and the purchases made
today with a void action.

**Corrections never edit.** The ledger is append-only and the purchase row is
the durable record of a void — `voided_at` and `void_reason` on
`membership_purchases` — while `membership_stamps` and `membership_rewards` hold
current state. Voiding a purchase writes a compensating `adjust` entry and
deletes the stamps and any unredeemed rewards it minted, so the slot is free
again. The record that the stamp was taken and given back is still in the
purchase row and the ledger, exactly as cancelling a booking deletes
`booking_slots` and leaves the booking behind. A gift already handed over cannot
be un-redeemed — it can only be compensated, which is what the `adjust` entry is
for.

## Franchise

`outlet_id` is added to `loyalty_entries` here, and carried on every new table.
`design/kiosk.md` says to add it before there is data to migrate; there still is
not, so this is the cheap moment. The ledger stays pooled — a member earns in
Jajag and redeems at Dimsamcong — and `outlet_id` is for attribution and
settlement, not isolation.

## Not in this change

- **The booth earning by itself.** It needs per-booth identity, which is the open
  question in `design/kiosk.md`, and its captured number is exactly the unverified
  case above. The counter is what ships.
- **Points for other verticals.** Dimsamcong earns nothing yet, so the pooled
  balance is a promise the schema keeps and the UI does not show.
- **Expiry, tiers, transferable gifts, a PIN.** All reachable, none wanted yet.

## Files

- `api/internal/store/migrations/0010_membership.sql`
- `api/internal/membership/` — the service, and its tests
- `api/internal/admin/` — `/stamps` and the redeem and void actions
- `api/internal/httpapi/membership.go` — the public lookup
- `api/cmd/bykami` — `membership import`
- `sites/root/src/pages/kartu.astro` — the customer's page on the brand domain

## How the change is judged

Not by uptime. By **staff-minutes per stamp**, against a pen: if writing a stamp
in the console takes longer than ticking a card, the paper card wins and this
work has failed. The second measure is the migration — whether the cards in
customers' hands become rows without anyone re-registering.
