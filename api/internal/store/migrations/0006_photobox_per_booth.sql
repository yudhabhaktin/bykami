-- Y2K, Vintage and Maroon are three booths, not three backdrops on one machine.
--
-- 0004 modelled them as three services sharing a single `photobox` resource, and
-- a resource is an availability pool. So booking Y2K at 14:00 took Vintage and
-- Maroon off sale at 14:00 as well — two thirds of the booth capacity withdrawn
-- for a reason nobody at the desk could explain, and the same class of mistake
-- 0004's own comment describes the old booking tool making.
--
-- It also made the calendar mapping unsayable. A resource holds exactly one
-- google_calendar_id, and the booths are run from two different Google accounts:
-- Y2K from the studio's, Vintage and Maroon from the booth account's. One row
-- cannot hold two calendars.
--
-- The new resource ids are the service ids they carry. That is not a collision
-- to be embarrassed about — each booth sells exactly one package today, so the
-- pairing is what turns every repointing below into a rename rather than a
-- lookup table. A booth that starts selling a second package later keeps its
-- resource id and simply gains a row in booking_services.

-- Guarded on the old row existing, because a database created after this
-- migration ships has never had a `photobox` resource: it gets the three booths
-- from the seed instead, and this file must then do nothing at all.
INSERT INTO booking_resources (id, name, google_calendar_id, active, created_at)
SELECT 'photobox-y2k', 'Photobox Y2K',
       -- The studio's account is where an already-connected photobox calendar
       -- would live, and Y2K is the booth staying on that account. Carrying it
       -- here keeps a working connection working; giving it to all three would
       -- rebuild the shared pool this migration exists to take apart, because
       -- one calendar's busy ranges would block all three booths.
       (SELECT google_calendar_id FROM booking_resources WHERE id = 'photobox'),
       1, unixepoch()
WHERE EXISTS (SELECT 1 FROM booking_resources WHERE id = 'photobox');

INSERT INTO booking_resources (id, name, google_calendar_id, active, created_at)
SELECT 'photobox-vintage', 'Photobox Vintage', '', 1, unixepoch()
WHERE EXISTS (SELECT 1 FROM booking_resources WHERE id = 'photobox');

INSERT INTO booking_resources (id, name, google_calendar_id, active, created_at)
SELECT 'photobox-maroon', 'Photobox Maroon', '', 1, unixepoch()
WHERE EXISTS (SELECT 1 FROM booking_resources WHERE id = 'photobox');

-- Each service moves to the booth of the same name.
UPDATE booking_services
   SET resource_id = id
 WHERE resource_id = 'photobox'
   AND id IN ('photobox-y2k', 'photobox-vintage', 'photobox-maroon');

-- Bookings follow their service. Read through booking_services, which the
-- statement above has already moved, so this needs no second list of ids.
UPDATE bookings
   SET resource_id = (SELECT s.resource_id FROM booking_services s WHERE s.id = bookings.service_id)
 WHERE resource_id = 'photobox';

-- And the slots follow their booking. No primary key can collide here: two rows
-- that survived (resource_id, starts_at) as 'photobox' had different starts_at,
-- and they keep them.
UPDATE booking_slots
   SET resource_id = (SELECT b.resource_id FROM bookings b WHERE b.id = booking_slots.booking_id)
 WHERE resource_id = 'photobox';

-- A closure that shut "the photobox" meant all of it, so it becomes three
-- closures. The id is derived rather than random because a migration that runs
-- twice on a repaired database should collide loudly on the primary key instead
-- of quietly closing the booths twice over.
INSERT INTO booking_blackouts (id, resource_id, starts_at, ends_at, reason, created_at)
SELECT id || '-y2k', 'photobox-y2k', starts_at, ends_at, reason, created_at
  FROM booking_blackouts WHERE resource_id = 'photobox';
INSERT INTO booking_blackouts (id, resource_id, starts_at, ends_at, reason, created_at)
SELECT id || '-vintage', 'photobox-vintage', starts_at, ends_at, reason, created_at
  FROM booking_blackouts WHERE resource_id = 'photobox';
INSERT INTO booking_blackouts (id, resource_id, starts_at, ends_at, reason, created_at)
SELECT id || '-maroon', 'photobox-maroon', starts_at, ends_at, reason, created_at
  FROM booking_blackouts WHERE resource_id = 'photobox';
DELETE FROM booking_blackouts WHERE resource_id = 'photobox';

-- The cached busy ranges and the sync record belong to the calendar the old
-- resource pointed at, and say nothing about three booths. Dropped rather than
-- copied, for the same reason SetCalendar drops them when a calendar is
-- re-pointed: stale ranges block times that are actually free. The next poll
-- repopulates whatever is connected.
DELETE FROM booking_calendar_busy WHERE resource_id = 'photobox';
DELETE FROM booking_calendar_sync WHERE resource_id = 'photobox';

-- Nothing may still point at it. Foreign keys are on, so if anything does —
-- a booking on a photobox service this migration does not know about — this
-- fails and rolls the whole migration back, which is the outcome to want.
DELETE FROM booking_resources WHERE id = 'photobox';
