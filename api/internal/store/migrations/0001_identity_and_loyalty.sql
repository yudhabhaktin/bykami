-- Identity and loyalty, created together on purpose.
--
-- The architecture record is explicit that booking must be built against shared
-- identity from day one, because shipping a vertical with its own user table
-- means a migration and forcing every existing customer to re-register when
-- loyalty launches. So the user model lands first, and booking will reference
-- it rather than the other way round.

CREATE TABLE users (
  id         TEXT    PRIMARY KEY,
  -- E.164, normalised by internal/phone before it ever reaches here. UNIQUE is
  -- the whole point: the phone number is the account, so two spellings of one
  -- number must collide rather than quietly become two balances.
  phone      TEXT    NOT NULL UNIQUE,
  -- Optional by design. Identity is phone-first to match Indonesian norms, and
  -- a booking needs nothing else. Collecting more by default is PII we would
  -- then have to protect for no benefit.
  name       TEXT,
  email      TEXT,
  created_at INTEGER NOT NULL
) STRICT;

-- One-time codes. The code itself is never stored — only a hash — so a database
-- read cannot be replayed into a login. Short-lived rows, swept after use.
CREATE TABLE otp_challenges (
  id          TEXT    PRIMARY KEY,
  phone       TEXT    NOT NULL,
  code_hash   BLOB    NOT NULL,
  expires_at  INTEGER NOT NULL,
  attempts    INTEGER NOT NULL DEFAULT 0,
  consumed_at INTEGER,
  created_at  INTEGER NOT NULL
) STRICT;

-- Supports both the rate-limit lookup (how many challenges for this number
-- recently) and the verify lookup (newest unconsumed challenge for this number).
CREATE INDEX idx_otp_phone_created ON otp_challenges (phone, created_at DESC);

-- Sessions are scoped to .bykami.id so one login works across every vertical.
-- Only the hash is stored, for the same reason as the OTP code.
CREATE TABLE sessions (
  token_hash BLOB    PRIMARY KEY,
  user_id    TEXT    NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_sessions_user ON sessions (user_id);

-- The loyalty ledger.
--
-- Append-only, never a mutable balance column. A balance is SUM(points) for a
-- user. Mutable totals drift, cannot be audited, and produce disputes that
-- cannot be resolved; an append-only ledger makes every point traceable to the
-- event that created it, and a bug is correctable with a compensating entry
-- rather than a manual balance edit.
CREATE TABLE loyalty_entries (
  id              TEXT    PRIMARY KEY,
  -- Deliberately not ON DELETE CASCADE. A cascade here would always collide
  -- with the immutability trigger below and abort with a message about
  -- append-only ledgers, which is a confusing way to learn that the real answer
  -- is "you cannot delete a customer who has loyalty history". Plain NO ACTION
  -- says that directly, as a foreign key violation. Erasure, when it is needed,
  -- is anonymising the user row — not deleting the entries that reference it.
  user_id         TEXT    NOT NULL REFERENCES users (id),
  -- Which vertical the points moved in. Cross-vertical by design: earn on a
  -- photo session, spend on dimsum.
  vertical        TEXT    NOT NULL,
  kind            TEXT    NOT NULL,
  -- Signed. Earning is positive, burning is negative, and the CHECK below makes
  -- that a property of the table rather than a habit of the callers.
  points          INTEGER NOT NULL,
  reference_id    TEXT,
  idempotency_key TEXT,
  created_at      INTEGER NOT NULL,

  CONSTRAINT loyalty_kind_known
    CHECK (kind IN ('earn', 'burn', 'adjust')),

  -- Sign follows kind. Without this an 'earn' of -500 is accepted and the
  -- ledger's vocabulary stops meaning anything. 'adjust' is the deliberate
  -- exception: a compensating entry has to be able to go either way, which is
  -- the whole reason it exists.
  CONSTRAINT loyalty_sign_matches_kind
    CHECK (
      (kind = 'earn'   AND points > 0) OR
      (kind = 'burn'   AND points < 0) OR
      (kind = 'adjust' AND points <> 0)
    ),

  -- Earning must be idempotent: a retried payment webhook must not credit
  -- twice. Requiring the key on 'earn' means the caller cannot opt out of the
  -- guarantee by omitting it.
  CONSTRAINT loyalty_earn_needs_idempotency_key
    CHECK (kind <> 'earn' OR idempotency_key IS NOT NULL)
) STRICT;

-- The constraint that actually delivers the guarantee. Application-level
-- "check then insert" loses to concurrent requests; a unique index does not,
-- and the second writer gets a constraint violation instead of a second credit.
-- Partial, so the many entries without a key do not collide with each other.
CREATE UNIQUE INDEX idx_loyalty_idempotency
  ON loyalty_entries (idempotency_key)
  WHERE idempotency_key IS NOT NULL;

CREATE INDEX idx_loyalty_user ON loyalty_entries (user_id);

-- Append-only enforced by the database, not by convention.
--
-- Every rule above is worthless if a later UPDATE can rewrite history — and the
-- most likely author of that UPDATE is a well-meaning fix for a support ticket.
-- These triggers make the intended correction (a compensating 'adjust' entry)
-- the only one available.
CREATE TRIGGER loyalty_entries_are_immutable
BEFORE UPDATE ON loyalty_entries
BEGIN
  SELECT RAISE(ABORT, 'loyalty_entries is append-only: insert a compensating adjust entry instead of updating');
END;

CREATE TRIGGER loyalty_entries_are_permanent
BEFORE DELETE ON loyalty_entries
BEGIN
  SELECT RAISE(ABORT, 'loyalty_entries is append-only: insert a compensating adjust entry instead of deleting');
END;
