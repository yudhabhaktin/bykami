-- The download link behind the QR on the delivery screen.
--
-- A capability, not an identifier. The session id is written to the log on
-- every capture, stamped on print jobs and shown in the operator's view of the
-- room; this token shows whoever holds it a stranger's photographs. They are
-- minted independently so that reading one of them anywhere never yields the
-- other.
--
-- The unguessable URL *is* the access control, which is the same decision
-- design/kiosk.md took for the cloud gallery: customers paste these links into
-- WhatsApp groups, that is wanted, and an expiry is the only control that
-- survives it. Here the expiry is not a separate mechanism — the link dies when
-- the purge deletes the photos behind it, seven days after capture.
ALTER TABLE sessions ADD COLUMN share_token TEXT;

-- Partial, because every session before this migration has NULL and SQLite
-- treats NULLs as distinct in a unique index anyway. Stated explicitly so the
-- intent survives someone reading it later: at most one session per token,
-- and any number of sessions with no token at all.
CREATE UNIQUE INDEX idx_sessions_share_token
  ON sessions (share_token) WHERE share_token IS NOT NULL;
