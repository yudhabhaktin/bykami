-- Authenticators for the operator console.
--
-- The console's login was the same one-time-code flow customers use, and that
-- flow needs a WhatsApp provider account which does not exist. The consequence
-- was recorded in api/README.md for months: nobody could sign in to the console
-- on the deployed box at all, and the frame catalogue grew a shell subcommand
-- to work around it. This table is the way in that needs nobody's provider —
-- a secret agreed once, and six digits computed from the clock after that.
--
-- Enrolment is a shell subcommand, `bykami admin enroll`, and has to be: doing
-- it in the console would need a login, which is the thing that does not exist
-- yet. Same shape as `bykami frames import` and for the same reason.
--
-- # Why this is not a permissions table
--
-- It is not who may use the console. That is still -admin-phones, checked on
-- every request, for the reasons in internal/admin — a role in the database has
-- a bootstrap problem whose usual answer is a seed script that quietly becomes
-- a way to grant admin. A row here proves possession of a phone; the allow-list
-- decides whether that is worth anything. Enrolling somebody who is not on the
-- list grants them precisely nothing, which is the property that makes it safe
-- for enrolment to be a shell command rather than a ceremony.
CREATE TABLE admin_totp (
  -- E.164, normalised by internal/phone before it reaches here, matching
  -- users.phone. Deliberately not a foreign key onto users: an operator need
  -- not be a customer, and the console must stay reachable when the users table
  -- is empty. The number is the join, not a row.
  phone        TEXT    PRIMARY KEY,

  -- The shared secret, as the twenty raw bytes RFC 4226 asks for.
  --
  -- Stored as it is used, which is the uncomfortable part and worth stating
  -- plainly rather than discovering later: unlike otp_challenges.code_hash this
  -- cannot be hashed, because both ends have to compute the same number from
  -- it. Anyone who can read this table can generate an operator's codes.
  --
  -- What that is worth is bounded by the paragraph above. A secret here is not
  -- authorisation; it produces valid codes for a number that still has to be in
  -- -admin-phones, which lives in the service configuration and not in this
  -- file. And anyone reading this table already has the database, which holds
  -- the sessions those codes would have produced anyway.
  secret       BLOB    NOT NULL,

  -- The last time step accepted for this operator, as RFC 6238 counts them:
  -- unix seconds divided by thirty. NULL until the first successful sign-in.
  --
  -- This is the replay guard, and it is the reason the column exists rather
  -- than a plain "last used" timestamp. A code stays valid for the rest of its
  -- period and one step either side of it, so without recording which step was
  -- spent, a number read over somebody's shoulder — or out of a shell history,
  -- or off a screen share — can be used again by somebody else for up to a
  -- minute and a half. A step is accepted only if it is later than this one.
  --
  -- It also doubles as the last-used timestamp, exactly: multiply by thirty.
  last_step    INTEGER,

  -- Consecutive failures, reset by any success. Six digits across a
  -- three-step window is one guess in about three hundred thousand, which is
  -- only safe while guessing is slow — a login form with no counter is an
  -- offline attack conducted online.
  fail_count   INTEGER NOT NULL DEFAULT 0,

  -- Unix seconds until which this operator's codes are refused outright, set
  -- when the failures run out. NULL when not locked.
  --
  -- Locking the enrolment rather than the connection, deliberately: an
  -- attacker's rate limit should not depend on where they are dialling from,
  -- and the console sits behind a tunnel where every request arrives from the
  -- same place anyway. The cost is that somebody who can guess an operator's
  -- number can lock them out for a quarter of an hour, which is an annoyance
  -- that a shell on the box can undo, and the alternative is worse.
  locked_until INTEGER,

  created_at   INTEGER NOT NULL,

  CONSTRAINT admin_totp_phone_is_e164
    CHECK (phone GLOB '+[0-9]*'),

  -- Twenty bytes, as generated. A shorter secret is one that arrived from
  -- somewhere other than the enrolment command.
  CONSTRAINT admin_totp_secret_is_full_length
    CHECK (length(secret) = 20),

  CONSTRAINT admin_totp_fail_count_is_not_negative
    CHECK (fail_count >= 0)
) STRICT;
