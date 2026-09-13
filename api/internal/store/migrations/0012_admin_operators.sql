-- Operator access management. Migration 0011 was already applied to the
-- production database, so this builds on the schema it leaves behind.
--
-- The console's login gains a username — the credential's label — and a page
-- where a manager manages who may use it. The identity is the label and the
-- password proves it; they are the same thing, not two columns.

-- can_manage separates an operator who may write stamps from one who may
-- create other operators. It defaults to 0 so a schema upgrade cannot
-- promote anybody who was not already a manager.
ALTER TABLE admin_credentials ADD COLUMN can_manage INTEGER NOT NULL DEFAULT 0;

-- created_by records the handover, because whoever generates a password
-- knows it and that is worth a row rather than a shrug.
ALTER TABLE admin_credentials ADD COLUMN created_by TEXT;

-- disabled_by is who ended somebody's access. A disabled row is not deleted
-- so that it can still be audited, and this column says who made that call.
ALTER TABLE admin_credentials ADD COLUMN disabled_by TEXT;

-- must_change is 1 for every new credential, including the first one created
-- from the shell, so the password printed on the box is never the password
-- anybody keeps. It is cleared when the operator sets their own.
ALTER TABLE admin_credentials ADD COLUMN must_change INTEGER NOT NULL DEFAULT 1;

-- changed_at records when the operator last set their own password.
ALTER TABLE admin_credentials ADD COLUMN changed_at INTEGER;

-- reset_by and reset_at record who generated a fresh one-time password for
-- this credential and when. The reason is attribution: without this, whoever
-- hands over a generated password can sign in as that person, which makes
-- that person's writes deniable.
ALTER TABLE admin_credentials ADD COLUMN reset_by TEXT;
ALTER TABLE admin_credentials ADD COLUMN reset_at INTEGER;

-- lookup stays UNIQUE. It used to be how the password was found; now it is
-- what stops the same password being issued to two people, which is exactly
-- what keeps attribution honest.
