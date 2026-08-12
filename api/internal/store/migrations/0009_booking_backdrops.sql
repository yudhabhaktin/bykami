-- Which wall the room is dressed in.
--
-- The studio shoots self photo against a plain paper roll it changes by hand and
-- pas foto against one of four flat colours, and until now the booking said
-- nothing about which. The operator found out when the customer walked in, which
-- is the wrong moment: hanging a roll takes a few minutes and the session is
-- five. So the choice moves into the booking, and the console prints it beside
-- the name — that is the whole point of this migration, and the reason the
-- column lands on `bookings` rather than being left as free text in `notes`.
--
-- Three tables' worth of shape for one dropdown, and each part earns its place:
-- the walls exist independently of any package (two lines sell the same white),
-- which package offers which is an editorial decision that changes when the
-- studio buys a backdrop, and what one customer picked is a fact about one
-- booking. Folding any pair together would mean repainting a wall in as many
-- places as there are packages that use it.

-- The walls themselves. Rows and not an enum in Go for the same reason
-- booking_resources is a table: how many there are is a fact about the building,
-- and it changes when somebody buys a roll of paper.
--
-- No colour value here, deliberately. A hex for "Duck Egg" would be a number
-- invented in a migration and then shown to a customer as though it were the
-- wall they are getting, and the sites are monochrome by design — six colour
-- chips would be the only hue on four properties. The name is what the studio
-- says out loud and what the operator hangs.
CREATE TABLE booking_backdrops (
  id         TEXT    PRIMARY KEY,
  name       TEXT    NOT NULL,
  -- Withdrawn rather than deleted, because bookings point at these and a booking
  -- is a record of what was agreed. A wall that is taken out of service stops
  -- being offered and stays readable on last month's sessions.
  active     INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,

  CONSTRAINT booking_backdrops_id_is_a_slug
    CHECK (id <> '' AND id NOT GLOB '*[^a-z0-9-]*')
) STRICT;

-- Which package may be shot against which wall.
--
-- Many-to-many because both directions are real: MINI through BIG MAXI share one
-- set of six plain rolls, and white is sold to self photo and to pas foto alike.
-- A package with no rows here offers no choice at all, which is the right answer
-- for the photobox — a booth's backdrop is built into the booth, and Y2K,
-- Vintage and Maroon are already three separate packages naming it.
--
-- order_index sits here and not on booking_backdrops because the order is a
-- property of the pairing rather than of the wall. Self photo leads with white
-- and pas foto leads with red and blue, which are the two an Indonesian identity
-- photo actually calls for; one global ordering cannot say both.
CREATE TABLE booking_service_backdrops (
  service_id  TEXT    NOT NULL REFERENCES booking_services (id),
  backdrop_id TEXT    NOT NULL REFERENCES booking_backdrops (id),
  order_index INTEGER NOT NULL DEFAULT 0,

  PRIMARY KEY (service_id, backdrop_id)
) STRICT;

-- The reverse lookup: everything that would stop using a wall if it were
-- withdrawn. The primary key already serves service_id.
CREATE INDEX idx_booking_service_backdrops_backdrop
  ON booking_service_backdrops (backdrop_id);

-- What this customer chose. NULL where the package offers nothing to choose, and
-- NULL on every booking taken before this existed — which is why it is nullable
-- rather than defaulted to some wall nobody picked.
--
-- A foreign key and not the name as text: the console reads it back through a
-- join, so a wall renamed from "Grey" to "Abu-abu" renames on old bookings too,
-- and that is correct here in a way it is not for the customer's own name. The
-- name on a booking records who turned up; this records which roll to hang, and
-- the roll has not changed.
--
-- SQLite allows ADD COLUMN with a REFERENCES clause only when the default is
-- NULL, which this is, so foreign_keys = ON does not have to be turned off to
-- apply it.
ALTER TABLE bookings
  ADD COLUMN backdrop_id TEXT REFERENCES booking_backdrops (id);
