-- The booth's local database.
--
-- This is not a cache of the cloud's tables. It is the authoritative record of
-- what happened in the room — which frames were taken, which were attributed to
-- whom, what was printed, and what consent was given — for the seven days
-- before the originals are purged. The cloud never sees most of it.

CREATE TABLE sessions (
  id         TEXT    PRIMARY KEY,
  -- Present from the first migration rather than added when the second outlet
  -- opens. The architecture record is explicit that the ledger stays pooled
  -- across franchise outlets, and a column added after there is data to migrate
  -- is the expensive version of this decision.
  outlet_id  TEXT    NOT NULL,
  state      TEXT    NOT NULL,

  -- What the customer bought, from the embedded catalogue. Copied onto the row
  -- rather than referenced, because the catalogue is content that will change
  -- and a session must still say what it was six weeks later.
  package_id    TEXT    NOT NULL,
  package_name  TEXT    NOT NULL,
  price_idr     INTEGER NOT NULL,
  template_id   TEXT    NOT NULL,
  print_copies  INTEGER NOT NULL,
  -- "maksimal 15x take", enforced at capture now that the app owns the shutter.
  -- Stored per session rather than read from config at check time, so that
  -- changing the default cannot retroactively change what an open session is
  -- allowed to do.
  take_limit INTEGER NOT NULL,
  opened_at  INTEGER NOT NULL,
  paid_at    INTEGER,
  closed_at  INTEGER,

  -- Delivery. The number is captured at the moment of peak delight and stored
  -- UNVERIFIED — loyalty credits only once it is verified through the cloud's
  -- OTP flow, so the append-only ledger stays clean.
  phone             TEXT,
  consent_version   TEXT,
  consented_at      INTEGER,
  -- Separate from the required one. Two purposes, therefore two consents;
  -- bundling them is the most common PDP mistake. Neither is pre-ticked in the
  -- UI, and this column defaulting to 0 is the same statement in the schema.
  marketing_consent INTEGER NOT NULL DEFAULT 0,

  -- The first state is the shutter's safety catch. A booth is self-service:
  -- nobody stands between the customer and the camera, so 'awaiting_payment' is
  -- what stops a stranger walking up and taking a free session. It becomes
  -- 'open' only when a payment settles.
  --
  -- 'abandoned' is a state rather than a deleted row. A customer who walks away
  -- from the QR code has a payment attempt behind them, and that attempt can
  -- still settle a minute later at the gateway — so the row it points at has to
  -- survive. Deleting the session would either orphan the charge or destroy the
  -- record of money that moved.
  CONSTRAINT session_state_known
    CHECK (state IN ('awaiting_payment', 'open', 'closed', 'abandoned')),

  CONSTRAINT session_take_limit_positive
    CHECK (take_limit > 0),

  CONSTRAINT session_price_not_negative
    CHECK (price_idr >= 0),

  -- closed_at and the state cannot disagree. A closed session with no timestamp
  -- makes the grace window uncomputable, and an open one with a timestamp is a
  -- row that already lost track of itself.
  CONSTRAINT session_closed_at_matches_state
    CHECK ((state IN ('closed', 'abandoned')) = (closed_at IS NOT NULL)),

  -- An open or closed session was paid for. Deriving this from the payments
  -- table instead would make "was this session paid?" a join that some future
  -- query forgets to write.
  CONSTRAINT session_open_means_paid
    CHECK (state IN ('awaiting_payment', 'abandoned') OR paid_at IS NOT NULL),

  -- A phone number cannot be recorded without the consent that permits it.
  -- This is the PDP obligation expressed where it cannot be forgotten: not in
  -- the handler that happens to collect it today, but in the table, so that
  -- every future writer inherits it. "They agreed to something at some point"
  -- is not a record, so the version is required too.
  CONSTRAINT session_phone_needs_consent
    CHECK (phone IS NULL OR (consent_version IS NOT NULL AND consented_at IS NOT NULL)),

  CONSTRAINT session_marketing_consent_boolean
    CHECK (marketing_consent IN (0, 1))
) STRICT;

-- Only one session may be live at a time: there is one camera, one screen and
-- one customer in the room, and a second live session would make attribution of
-- an incoming file ambiguous — which is the one thing the hot-folder design has
-- to get right. A partial unique index over a constant is the standard way to
-- say "at most one row matching this predicate".
--
-- A session awaiting payment counts as live. Otherwise the customer standing at
-- the QR code loses the booth to whoever taps the screen next.
CREATE UNIQUE INDEX idx_sessions_single_live
  ON sessions ((1)) WHERE state IN ('awaiting_payment', 'open');

CREATE INDEX idx_sessions_opened ON sessions (opened_at DESC);

-- Payments.
--
-- Recorded on the booth even though the money moves in the cloud, because the
-- booth is what has to decide whether to release the shutter. A booth that has
-- to reach the network to answer "has this customer paid?" stops working the
-- moment the network does, mid-session, with a paying customer in front of it.
CREATE TABLE payments (
  id         TEXT    PRIMARY KEY,
  session_id TEXT    NOT NULL REFERENCES sessions (id),
  provider   TEXT    NOT NULL,
  -- The provider's own id for this charge. UNIQUE because it is the
  -- idempotency key: a status poll that arrives twice, or a retried create,
  -- must not become two charges against one session.
  external_id TEXT   NOT NULL UNIQUE,
  amount_idr  INTEGER NOT NULL,
  -- The QRIS payload the customer's banking app scans. Held only until the
  -- payment settles or expires; it is worthless afterwards and it is one more
  -- thing sitting on a PC in a public room.
  qr_payload  TEXT,
  state       TEXT    NOT NULL,
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL,
  settled_at  INTEGER,

  CONSTRAINT payment_state_known
    CHECK (state IN ('pending', 'settled', 'expired', 'failed')),

  CONSTRAINT payment_amount_positive
    CHECK (amount_idr > 0),

  CONSTRAINT payment_settled_has_time
    CHECK ((state = 'settled') = (settled_at IS NOT NULL))
) STRICT;

CREATE INDEX idx_payments_session ON payments (session_id);
CREATE INDEX idx_payments_pending ON payments (expires_at) WHERE state = 'pending';

CREATE TABLE photos (
  id           TEXT    PRIMARY KEY,
  -- NULL is meaningful: an orphan. Staff test shots and accidental fires land
  -- outside any session, and they are kept and shown in admin rather than
  -- discarded, because a customer's frame that missed its session by a second
  -- is indistinguishable from a test shot until a human looks.
  session_id   TEXT    REFERENCES sessions (id),

  -- SHA-256 of the file's bytes, and the reason a crash-recovery rescan is
  -- safe. The agent rescans at startup for files absent from this table; with
  -- content as the key, re-ingesting a file it already has is a constraint
  -- violation to swallow rather than a duplicate row to clean up later.
  content_hash TEXT    NOT NULL UNIQUE,

  -- Relative to the store root, never absolute. The booth PC's drive letter is
  -- not a fact worth persisting, and a relative path survives the directory
  -- being moved or restored somewhere else.
  path         TEXT    NOT NULL,
  bytes        INTEGER NOT NULL,
  width        INTEGER NOT NULL,
  height       INTEGER NOT NULL,
  source       TEXT    NOT NULL,

  -- Filesystem mtime, never EXIF. Camera clocks drift and nobody resets them
  -- after a battery change, so EXIF is metadata and this is the truth used for
  -- attributing a file to a session.
  captured_at  INTEGER NOT NULL,
  ingested_at  INTEGER NOT NULL,

  -- The delivered derivative: long edge 2048, EXIF stripped. Separate from the
  -- original because printing from it would give back exactly what full
  -- resolution capture was for.
  derived_path TEXT,
  derived_at   INTEGER,
  uploaded_at  INTEGER,
  -- Set when the original is deleted at 7 days. The row outlives the file: it
  -- is how the agent knows not to re-ingest, and how a dispute is answerable
  -- after the bytes are gone.
  purged_at    INTEGER,

  CONSTRAINT photo_source_known
    CHECK (source IN ('hotfolder', 'webcam')),

  CONSTRAINT photo_dimensions_positive
    CHECK (width > 0 AND height > 0)
) STRICT;

CREATE INDEX idx_photos_session ON photos (session_id, captured_at);

-- Drives the 7-day purge without scanning the whole table.
CREATE INDEX idx_photos_unpurged ON photos (ingested_at) WHERE purged_at IS NULL;

-- The print queue exists because a browser cannot have one. window.print() is
-- fire-and-forget by design: no job status, no error, no media remaining, and
-- running out of media mid-session with no signal is the failure that loses a
-- customer.
CREATE TABLE print_jobs (
  id          TEXT    PRIMARY KEY,
  session_id  TEXT    NOT NULL REFERENCES sessions (id),
  layout      TEXT    NOT NULL,
  -- The composed sheet, relative to the store root. Persisted rather than held
  -- in memory because a queued job has to survive the agent restarting, and a
  -- job whose image the process forgot is a job that can only fail.
  sheet_path  TEXT    NOT NULL,
  copies      INTEGER NOT NULL,
  -- Sheets consumed, which is not the same as copies: the printer's native
  -- 2-inch cut yields two 2x6 strips from one 4x6 sheet. Recorded as the number
  -- actually taken off the roll, because that is what the media counter needs.
  sheets      INTEGER NOT NULL,
  state       TEXT    NOT NULL,
  error       TEXT,
  queued_at   INTEGER NOT NULL,
  started_at  INTEGER,
  finished_at INTEGER,

  CONSTRAINT print_layout_known
    CHECK (layout IN ('4r', 'strip2x6', '6x8')),

  CONSTRAINT print_state_known
    CHECK (state IN ('queued', 'printing', 'done', 'failed')),

  CONSTRAINT print_copies_positive
    CHECK (copies > 0 AND sheets > 0),

  -- A failed job must say why. "It failed" is not actionable at 9pm at an event
  -- with a queue of people waiting.
  CONSTRAINT print_failure_has_reason
    CHECK (state <> 'failed' OR error IS NOT NULL)
) STRICT;

CREATE INDEX idx_print_jobs_pending ON print_jobs (queued_at) WHERE state IN ('queued', 'printing');
CREATE INDEX idx_print_jobs_session ON print_jobs (session_id);

-- Media remaining, as a ledger rather than a counter.
--
-- Same reasoning as the loyalty ledger in the cloud: a mutable "sheets left"
-- column drifts, and the drift is discovered when the roll runs out mid-event
-- with the counter reading 200. Loading a roll appends a positive entry, every
-- sheet printed appends a negative one, and remaining is SUM(sheets).
CREATE TABLE media_entries (
  id         TEXT    PRIMARY KEY,
  kind       TEXT    NOT NULL,
  sheets     INTEGER NOT NULL,
  job_id     TEXT    REFERENCES print_jobs (id),
  note       TEXT,
  created_at INTEGER NOT NULL,

  CONSTRAINT media_kind_known
    CHECK (kind IN ('load', 'consume', 'adjust')),

  CONSTRAINT media_sign_matches_kind
    CHECK (
      (kind = 'load'    AND sheets > 0) OR
      (kind = 'consume' AND sheets < 0) OR
      (kind = 'adjust'  AND sheets <> 0)
    ),

  -- Every consumed sheet traces to the job that consumed it. Without this the
  -- counter can disagree with the queue and there is no way to tell which is
  -- wrong.
  CONSTRAINT media_consume_has_job
    CHECK (kind <> 'consume' OR job_id IS NOT NULL)
) STRICT;

CREATE INDEX idx_media_created ON media_entries (created_at);

-- Append-only, enforced by the database rather than by convention — the same
-- rule the loyalty ledger already carries, for the same reason. The most likely
-- author of an UPDATE here is a well-meaning fix for "the counter looks wrong".
CREATE TRIGGER media_entries_are_immutable
BEFORE UPDATE ON media_entries
BEGIN
  SELECT RAISE(ABORT, 'media_entries is append-only: insert a compensating adjust entry instead of updating');
END;

CREATE TRIGGER media_entries_are_permanent
BEFORE DELETE ON media_entries
BEGIN
  SELECT RAISE(ABORT, 'media_entries is append-only: insert a compensating adjust entry instead of deleting');
END;
