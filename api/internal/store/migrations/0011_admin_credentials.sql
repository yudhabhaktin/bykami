-- Password credentials for the operator console.
--
-- The console used to sign in with a phone number and a TOTP code from an
-- authenticator app. That needed no provider, which is why it existed — the
-- customer OTP flow was blocked on WhatsApp — but it also needed an app, a
-- QR code, and a phone per operator. This replaces it with one generated
-- password per operator, entered into a single field.
--
-- Enrolment is still a shell subcommand, and still has to be: doing it in the
-- console would need somebody already signed in to the console, which is the
-- thing that does not exist until the first credential does.

-- One operator credential. The password is generated, not chosen: 24 characters
-- from an alphabet without lookalikes, which is ~120 bits. There is no
-- dictionary to attack, so the KDF's iteration count is insurance rather than
-- the main defence.
--
-- lookup is the index (SHA-256 of the password) because a KDF output cannot be
-- searched. hash is what is verified. Both are checked, in that order: a
-- lookup that merely found a row would make a database read a login, because
-- the reader could add their own row and sign in. Verifying the KDF afterwards
-- means the index is useful for finding a candidate and useless as a credential.
CREATE TABLE admin_credentials (
  id TEXT PRIMARY KEY,
  -- The human name: "yudha", "kasir-1". Not a username — the credential is the
  -- identifier and the label is who the write is attributed to.
  label TEXT NOT NULL UNIQUE CHECK (LENGTH(label) > 0),
  -- SHA-256 of the password. Unique so that a password identifies exactly one
  -- credential, and BLOB because the output of a hash is bytes, not text.
  lookup BLOB NOT NULL UNIQUE,
  -- PBKDF2-HMAC-SHA256 of the password, with a random salt.
  hash BLOB NOT NULL,
  -- 16 random bytes, drawn once at creation. Stored with the hash so that
  -- verification can re-derive the same key.
  salt BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  -- Set on every successful verification, so a list command can show when a
  -- credential was last used without reading the session table.
  last_used_at INTEGER,
  -- Set by removal, not deletion: a deleted credential cannot be audited.
  -- Sessions belonging to a disabled credential stop working on the next
  -- request because SessionForToken joins against this table.
  disabled_at INTEGER,
  CHECK (disabled_at IS NULL OR disabled_at >= created_at)
) STRICT;

-- Sessions for the console. Opaque tokens, hashed before storage, exactly as
-- identity.sessions does: the plaintext exists only in the cookie.
--
-- Thirty days, unchanged from the previous console session shape, and the
-- cookie keeps its __Host- prefix: Secure, Path=/, no Domain, HttpOnly,
-- SameSite=Strict.
CREATE TABLE admin_sessions (
  token_hash BLOB PRIMARY KEY,
  credential_id TEXT NOT NULL REFERENCES admin_credentials (id),
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_admin_sessions_credential ON admin_sessions (credential_id);

-- Login attempts, counted per source address because a single password field
-- has no identifier to rate-limit against. Ten failures in fifteen minutes
-- locks that address for fifteen minutes. Rows are swept as they age.
CREATE TABLE admin_login_attempts (
  id TEXT PRIMARY KEY,
  -- The client IP as read from CF-Connecting-IP, X-Forwarded-For, or
  -- RemoteAddr. The same extraction the console uses on every request.
  ip TEXT NOT NULL,
  created_at INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_admin_login_attempts_ip ON admin_login_attempts (ip, created_at);
CREATE INDEX idx_admin_login_attempts_age ON admin_login_attempts (created_at);
