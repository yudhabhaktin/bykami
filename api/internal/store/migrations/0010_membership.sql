-- The studio's paper loyalty card, as a view over the ledger.
--
-- The card is a physical object: a name and a WhatsApp number on the front,
-- nine slots, five of them carrying a gift, ticked with a pen. This migration
-- is what replaces it, and it is not a second loyalty system — internal/loyalty
-- is already that, and platform-architecture.md already argues why there is one
-- ledger and not one per vertical. What lands here is the mapping: a card that
-- can fill and close, a gift that cannot be paid out twice, and the operator
-- surface's tables around them.
--
-- Two rules drive everything below, and both are the card's printed fine print
-- rather than a screen's opinion:
--
--   * A stamp is one multiple of Rp 45.000, floored per transaction. Rp 44.000
--     twice is zero stamps, not one. A balance cannot express a per-transaction
--     floor, which is why stamps are grouped into a card row rather than summed
--     out of the ledger.
--   * A gift earned today is redeemed on a later visit, never the same day.
--     That is a column (redeem_after) and a condition inside the reward's own
--     UPDATE, not a check in a handler.
--
-- Provenance is the third thing: design/platform-architecture.md says an
-- unverified number never earns, and that rule was written about the kiosk. The
-- counter is the opposite case — staff are looking at the customer and at the
-- phone in their hand when they write the stamp — so the entry records which
-- human wrote it, and membership_purchases carries the operator's number.

-- One sellable card program. A row rather than constants, because the price of
-- a stamp is a business fact that changes and a migration per price rise is not
-- a design.
CREATE TABLE membership_programs (
  id                    TEXT    PRIMARY KEY,
  -- The ledger vertical a stamp earns into. Cross-vertical by design: a member
  -- earns in Jajag and redeems at Dimsamcong, and the ledger stays pooled.
  vertical              TEXT    NOT NULL,
  -- What one stamp costs. Rp 45.000 today, and the number every price check
  -- divides by.
  amount_per_stamp_idr  INTEGER NOT NULL CHECK (amount_per_stamp_idr > 0),
  -- How many stamps fill the card. Nine, which is the card in customers' hands.
  stamps_per_card       INTEGER NOT NULL CHECK (stamps_per_card > 0),
  -- A program is retired, never deleted: purchases reference it, and history
  -- that cannot be read is history that cannot be defended.
  active                INTEGER NOT NULL CHECK (active IN (0, 1))
) STRICT;

-- The studio's card, as it is printed. One row because there is one program,
-- but the shape is a row so a second card design is data rather than a branch.
INSERT INTO membership_programs (id, vertical, amount_per_stamp_idr, stamps_per_card, active)
VALUES ('studio', 'studio', 45000, 9, 1);

-- Which slots carry a gift, and which gift. The label is stored rather than
-- derived from the kind, because "+5 menit" is the exact string on the card and
-- a template that reconstructed it would be a second place the wording lives.
CREATE TABLE membership_milestones (
  program_id  TEXT    NOT NULL REFERENCES membership_programs (id),
  stamp_no    INTEGER NOT NULL CHECK (stamp_no > 0),
  reward_kind TEXT    NOT NULL,
  label       TEXT    NOT NULL CHECK (LENGTH(label) > 0),
  -- One gift per slot: the primary key is what makes "the reward for stamp 4"
  -- a question with one answer.
  PRIMARY KEY (program_id, stamp_no),
  -- The gift vocabulary is closed. A typo in reward_kind is a gift nobody can
  -- hand over, discovered at the counter in front of the customer.
  CONSTRAINT membership_reward_kind_known
    CHECK (reward_kind IN ('extra_minutes_5', 'print_4r', 'photobox_one_person'))
) STRICT;

-- The ladder, exactly as printed on the card: 2, 4, 6, 8 and the ninth.
INSERT INTO membership_milestones (program_id, stamp_no, reward_kind, label) VALUES
  ('studio', 2, 'extra_minutes_5', '+5 menit'),
  ('studio', 4, 'print_4r', '1 print 4R'),
  ('studio', 6, 'extra_minutes_5', '+5 menit'),
  ('studio', 8, 'print_4r', '1 print 4R'),
  ('studio', 9, 'photobox_one_person', 'Free photobox 1 orang');

-- A milestone past the end of the card is a gift that can never be reached.
-- The database refuses the row rather than letting a configuration mistake
-- quietly promise a reward and never issue it.
CREATE TRIGGER membership_milestone_within_card BEFORE INSERT ON membership_milestones
WHEN NEW.stamp_no > (SELECT stamps_per_card FROM membership_programs WHERE id = NEW.program_id)
BEGIN SELECT RAISE(ABORT, 'membership_milestones: stamp_no is past the end of the card'); END;

-- The card in somebody's hand. Summing the ledger says how many stamps a person
-- has ever earned; it cannot say which of them are on this card, and therefore
-- cannot say when the card fills. That grouping is this table's whole job.
CREATE TABLE membership_cards (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users (id),
  program_id TEXT NOT NULL REFERENCES membership_programs (id),
  -- Which card this is for that member, counting from one. Part of the unique
  -- key below so that "card 3" is unambiguous for the rest of time.
  card_no INTEGER NOT NULL CHECK (card_no > 0),
  opened_at INTEGER NOT NULL,
  -- Set the moment the ninth stamp lands. Nothing expires when it is set: an
  -- unredeemed gift survives the card closing, which is the friendlier reading
  -- of a card that says nothing about expiry.
  closed_at INTEGER,
  UNIQUE (user_id, program_id, card_no),
  CHECK (closed_at IS NULL OR closed_at >= opened_at)
) STRICT;

-- At most one open card per member per program. This is what makes "which card
-- does this stamp belong to" unambiguous, and it is the reason stamp_no can be
-- computed as live+1 rather than searched for.
CREATE UNIQUE INDEX idx_membership_one_open_card
  ON membership_cards (user_id, program_id) WHERE closed_at IS NULL;

-- One stamping event: an amount, the stamps it earned, and who wrote them.
--
-- The row exists separately from the ledger entry because the ledger records
-- points and this records the transaction that produced them — amount, outlet,
-- operator, and the reference that makes the whole thing idempotent. The ledger
-- entry's reference names this purchase.
CREATE TABLE membership_purchases (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users (id),
  card_id TEXT NOT NULL REFERENCES membership_cards (id),
  -- The ledger entry that credited these stamps. Kept so that a compensation
  -- can name the original, and so a reconcile can join the two tables.
  entry_id TEXT NOT NULL REFERENCES loyalty_entries (id),
  amount_idr INTEGER NOT NULL CHECK (amount_idr > 0),
  -- floor(amount / amount_per_stamp_idr), per transaction. Stored rather than
  -- recomputed: the price of a stamp may change, and a purchase must keep
  -- meaning what it meant when it was written.
  stamps INTEGER NOT NULL CHECK (stamps > 0),
  -- The receipt or order reference, and the idempotency key's other half. Null
  -- is allowed because the schema should not depend on the caller having one.
  reference_id TEXT,
  -- Attribution, not isolation: the ledger is pooled across outlets, and this
  -- is what a settlement report groups by.
  outlet_id TEXT NOT NULL,
  -- Which human wrote the stamp. Provenance at the counter is a person, and
  -- this is the row that says which one.
  operator TEXT NOT NULL CHECK (LENGTH(operator) > 0),
  created_at INTEGER NOT NULL,
  voided_at INTEGER,
  void_reason TEXT,
  -- A void carries a reason or it is not a void. Corrections never edit: the
  -- purchase stays, marked, and a compensating adjust entry is written.
  CHECK (voided_at IS NULL OR (void_reason IS NOT NULL AND LENGTH(void_reason) > 0))
) STRICT;

-- The idempotency guarantee, and a unique index rather than a check-then-insert
-- because the latter loses to a double-tap. Partial on voided_at so that a
-- reference which was voided frees its key rather than blocking the correction.
CREATE UNIQUE INDEX idx_membership_purchase_reference
  ON membership_purchases (user_id, reference_id)
  WHERE reference_id IS NOT NULL AND voided_at IS NULL;
CREATE INDEX idx_membership_purchases_user ON membership_purchases (user_id, created_at DESC);
-- The console's "today" list, which is read far more often than it is written.
CREATE INDEX idx_membership_purchases_day ON membership_purchases (created_at DESC);

-- One slot, filled. stamp_no is the slot number on the card, so live stamps are
-- always 1..n and the next one is live+1. Voids may only ever happen at the
-- tail — see the service — which is what keeps that contiguity true.
CREATE TABLE membership_stamps (
  card_id TEXT NOT NULL REFERENCES membership_cards (id),
  stamp_no INTEGER NOT NULL CHECK (stamp_no > 0),
  -- A stamp imported from a paper card has no purchase behind it. Its
  -- provenance is the ledger entry whose reference is "paper:" plus the
  -- barcode, and purchase_id is NULL for those rows.
  purchase_id TEXT REFERENCES membership_purchases (id),
  created_at INTEGER NOT NULL,
  PRIMARY KEY (card_id, stamp_no)
) STRICT;

-- Finding the stamps that belong to a purchase, and the primary key above is
-- keyed on the card rather than the purchase.
CREATE INDEX idx_membership_stamps_purchase ON membership_stamps (purchase_id);

-- A gift, issued as a row with a state.
--
-- Model a gift as points and two staff members looking at the same phone number
-- can each spend the same one: both read a balance of one and neither write
-- contradicts the other. As a row, the transition is a conditional UPDATE whose
-- row count is the answer, so one success is the only possible outcome.
CREATE TABLE membership_rewards (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users (id),
  card_id TEXT NOT NULL REFERENCES membership_cards (id),
  stamp_no INTEGER NOT NULL CHECK (stamp_no > 0),
  reward_kind TEXT NOT NULL,
  label TEXT NOT NULL CHECK (LENGTH(label) > 0),
  issued_at INTEGER NOT NULL,
  -- The first instant it may be handed over: the WIB date it was issued, plus
  -- one day, as an epoch second. The same-day rule lives here rather than in a
  -- handler because it is the card's fine print.
  redeem_after INTEGER NOT NULL,
  redeemed_at INTEGER,
  -- Which operator handed it over. Required with redeemed_at by the CHECK
  -- below: a redeemed gift with no name against it is a payout nobody can
  -- account for.
  redeemed_by TEXT,
  -- One reward per slot, so a retried mint cannot issue a second gift.
  UNIQUE (card_id, stamp_no),
  CONSTRAINT membership_reward_kind_known
    CHECK (reward_kind IN ('extra_minutes_5', 'print_4r', 'photobox_one_person')),
  CHECK (redeemed_at IS NULL OR redeemed_by IS NOT NULL)
) STRICT;

CREATE INDEX idx_membership_rewards_user ON membership_rewards (user_id, issued_at DESC);
-- The console's outstanding list: unredeemed, oldest first. Partial so the
-- index only carries rows the list actually reads.
CREATE INDEX idx_membership_rewards_open
  ON membership_rewards (redeem_after) WHERE redeemed_at IS NULL;

-- Every card lookup by phone number.
--
-- The customer reaches the card with no session, no cookie and no provider: a
-- WhatsApp number is already written on the front of the card and is already the
-- account. What that costs is that a list of numbers can be walked through, so
-- the mitigation is rows the rate limit counts, swept as they age. Same shape as
-- identity's challenge limit, so there is one idea in the codebase about what a
-- rate limit is.
CREATE TABLE membership_lookups (
  id TEXT PRIMARY KEY,
  -- The normalised number that was asked about, and the address that asked.
  -- Both, because the two limits catch different attacks: one number guessed
  -- from everywhere, and one address walking a list.
  phone TEXT NOT NULL,
  ip TEXT NOT NULL,
  -- Whether a member was found. Recorded even when nobody was, because a
  -- walk-through of numbers that are not members is exactly the pattern these
  -- rows exist to make visible.
  found INTEGER NOT NULL CHECK (found IN (0, 1)),
  created_at INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_membership_lookups_phone ON membership_lookups (phone, created_at);
CREATE INDEX idx_membership_lookups_ip ON membership_lookups (ip, created_at);
-- The sweep: rows older than the retention window are deleted on the way past.
CREATE INDEX idx_membership_lookups_age ON membership_lookups (created_at);

-- Franchise attribution on the ledger itself.
--
-- design/kiosk.md says to add this before there is data to migrate, and there
-- still is not, so this is the cheap moment. The ledger stays pooled — one
-- balance across Jajag and Dimsamcong — and outlet_id is for attribution and
-- settlement, never for isolation. Nullable because every existing entry
-- predates the concept and guessing an outlet for them would be inventing a
-- fact.
ALTER TABLE loyalty_entries ADD COLUMN outlet_id TEXT;
