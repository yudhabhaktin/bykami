-- Repair installs that 0012 left managerless.
--
-- Migration 0012 added can_manage with DEFAULT 0, so a credential enrolled
-- from the shell was not a manager. The console has no way to promote anybody,
-- so an installed box could only be fixed from a shell on the box — and that
-- box has no inbound ssh. This migration promotes the earliest-created
-- non-disabled credential when no non-disabled manager exists. It is a no-op
-- on an empty table and on a table that already has a manager.

UPDATE admin_credentials
   SET can_manage = 1
 WHERE id = (
    SELECT id FROM admin_credentials
     WHERE disabled_at IS NULL
     ORDER BY created_at ASC, id ASC
     LIMIT 1
   )
   AND NOT EXISTS (
    SELECT 1 FROM admin_credentials
     WHERE can_manage = 1 AND disabled_at IS NULL
   );
