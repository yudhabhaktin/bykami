-- Booth access accounts for the tunnelled test deployment.
--
-- A username+password login replaces the previous URL-token access token.
-- Accounts live on the booth itself rather than in the cloud, because the
-- booth module shares no code with api/ on purpose.
--
-- The hash is PBKDF2-SHA256 with a per-account random salt. The iteration
-- count is stored so it can be raised later without breaking existing hashes.
-- The duplication of this scheme from api/internal/adminauth is deliberate:
-- the two modules share no code on purpose, and each owns its own credential
-- table.
CREATE TABLE booth_access_accounts (
  id         TEXT    PRIMARY KEY,
  username   TEXT    NOT NULL UNIQUE,
  -- Normalised: lowercase, trimmed.
  hash       BLOB    NOT NULL,
  salt       BLOB    NOT NULL,
  iterations INTEGER NOT NULL DEFAULT 600000,
  disabled_at INTEGER,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER
) STRICT;

-- Login attempt log for rate limiting. One row per failure.
CREATE TABLE booth_login_attempts (
  id         TEXT    PRIMARY KEY,
  ip         TEXT    NOT NULL,
  username   TEXT    NOT NULL,
  created_at INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_booth_login_attempts_ip ON booth_login_attempts (ip, created_at);
