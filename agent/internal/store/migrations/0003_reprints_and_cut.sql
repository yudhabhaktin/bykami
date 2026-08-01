-- The booth sells one session, and everything past the first print is bought
-- separately.
--
-- Until now a payment was the session and there was exactly one of them, so
-- "has this session been paid for" and "which payment is on screen" were the
-- same question. A paid reprint makes a second, third and fourth payment
-- against the same open session ordinary, and the two questions come apart: the
-- shutter is released by the session payment alone, while the print allowance
-- grows with every settled reprint.
--
-- Told apart by kind rather than by position. Counting "every settled payment
-- after the first" would give the same answer today and quietly become wrong
-- the moment a session payment can be retried after a failure.
ALTER TABLE payments ADD COLUMN kind TEXT NOT NULL DEFAULT 'session';

-- Every payment that existed before this migration opened a session, which is
-- what the default backfills them as.
CREATE INDEX idx_payments_session_kind ON payments (session_id, kind, state);

-- Whether the printer's blade splits the sheet.
--
-- The customer chooses this before the frame, because it is what they walk away
-- holding: cut gives the two 2x6 strips the booth format is named for, uncut
-- gives one 4x6 kept whole. A job has to remember it across an agent restart
-- for the same reason it remembers its composed sheet — a queued job the
-- process has forgotten the instructions for can only fail.
--
-- The media ledger is unaffected either way. Both feed one 4x6 sheet off the
-- roll; the difference is whether the machine cuts what it just printed.
--
-- Defaulted to cut, which is what every job before this migration did: the only
-- layout the booth has ever sold is strip2x6, and the printer's native 2-inch
-- cut is how two strips come off one sheet.
ALTER TABLE print_jobs ADD COLUMN cut INTEGER NOT NULL DEFAULT 1;
