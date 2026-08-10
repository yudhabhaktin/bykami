-- Booking, replacing two YouCanBook.me calendars.
--
-- The studio sold slots on `studiobykami-photobox` and `studiobykami-self`
-- until this landed. Both pages priced a session, took a name and a WhatsApp
-- number, and wrote the result into the owner's Google Calendar; the owner
-- worked out of that calendar and never out of the booking tool. So Google
-- Calendar stays where it is — this schema owns the booking, and the calendar
-- is read for what the owner blocks by hand and written to so the booking shows
-- up where they already look.
--
-- Two things here are load bearing and the rest is bookkeeping: the partial
-- unique index on (resource_id, starts_at), which is the only thing standing
-- between this and a double-booked room, and booking_calendar_busy being a
-- cache rather than a source. The second is the same rule the booth's frame
-- sync follows — a photo studio that stops taking bookings because an API in
-- Mountain View is slow has traded a real sale for a hypothetical conflict.

-- What can be occupied at one instant. Rows, not code, because how many there
-- are is a fact about the building and it has changed once already: the two
-- YouCanBook.me pages presented six choices while serving one shared
-- availability pool, so a photobox booking blocked a self-photo session that
-- could physically have run at the same time.
CREATE TABLE booking_resources (
  -- Slug, and the value a URL and a Google Calendar mapping are keyed on.
  id                 TEXT    PRIMARY KEY,
  name               TEXT    NOT NULL,
  -- The calendar this resource's bookings are written to and whose busy ranges
  -- block it. Empty means not connected yet, which is the normal state of a
  -- fresh deployment and must not be an error — availability then comes from
  -- this database alone.
  google_calendar_id TEXT    NOT NULL DEFAULT '',
  active             INTEGER NOT NULL DEFAULT 1,
  created_at         INTEGER NOT NULL,

  CONSTRAINT booking_resources_id_is_a_slug
    CHECK (id <> '' AND id NOT GLOB '*[^a-z0-9-]*')
) STRICT;

-- One sellable session: a price, a length, and how many people it is for.
CREATE TABLE booking_services (
  id               TEXT    PRIMARY KEY,
  resource_id      TEXT    NOT NULL REFERENCES booking_resources (id),
  name             TEXT    NOT NULL,
  -- Groups the packages under a heading on the booking page. Checked rather
  -- than free text — unlike frames.group_name, a typo here is not a mislabelled
  -- card, it is a package filed under a heading a customer is not reading.
  service_line     TEXT    NOT NULL,
  description      TEXT    NOT NULL DEFAULT '',
  price_idr        INTEGER NOT NULL,
  -- Photobox and Pas Foto Formal are priced per orang; every other package is a
  -- flat price for the group. Without this the page has to guess from the name,
  -- and a group of five reading 30K for a 150K session is a dispute at the desk.
  price_per_person INTEGER NOT NULL DEFAULT 0,

  -- How long the session actually runs, and the gap to leave after it. These
  -- are separate because they are separate facts: the booking page sold a
  -- 10-minute photobox session on a 30-minute grid, so two thirds of every slot
  -- was changeover. Folding them into one number loses the ability to tell a
  -- customer how long they have in the room.
  duration_minutes INTEGER NOT NULL,
  buffer_minutes   INTEGER NOT NULL DEFAULT 0,

  headcount_min    INTEGER NOT NULL DEFAULT 1,
  headcount_max    INTEGER NOT NULL DEFAULT 1,
  order_index      INTEGER NOT NULL DEFAULT 0,
  active           INTEGER NOT NULL DEFAULT 1,
  created_at       INTEGER NOT NULL,

  CONSTRAINT booking_services_id_is_a_slug
    CHECK (id <> '' AND id NOT GLOB '*[^a-z0-9-]*'),

  CONSTRAINT booking_services_line_known
    CHECK (service_line IN (
      'self-photo', 'photobox', 'pas-foto', 'outdoor-photographer', 'videographer'
    )),

  CONSTRAINT booking_services_has_a_duration
    CHECK (duration_minutes > 0 AND buffer_minutes >= 0),

  -- A band that runs backwards is a package nobody can book, and the symptom is
  -- an owner certain they published it.
  CONSTRAINT booking_services_headcount_runs_forwards
    CHECK (headcount_min >= 1 AND headcount_max >= headcount_min)
) STRICT;

CREATE INDEX idx_booking_services_live
  ON booking_services (active, resource_id, order_index);

-- When the studio is open, per weekday, in minutes past local midnight.
--
-- A table and not a constant because these hours are the first thing Ramadan
-- changes, and the alternative is a deploy to move a prayer break. `openingHours`
-- in the content package publishes "Mo-Su 09:00-21:00" from the same fact, so
-- these rows are also what keeps that line honest.
CREATE TABLE booking_hours (
  -- Go's time.Weekday: 0 is Sunday.
  weekday   INTEGER NOT NULL PRIMARY KEY,
  opens_at  INTEGER NOT NULL,
  closes_at INTEGER NOT NULL,

  CONSTRAINT booking_hours_weekday_is_a_weekday
    CHECK (weekday BETWEEN 0 AND 6),

  CONSTRAINT booking_hours_run_forwards
    CHECK (opens_at >= 0 AND closes_at > opens_at AND closes_at <= 1440)
) STRICT;

-- The recurring closures inside those hours: Dzuhur and Maghrib. Derived from
-- 31 days of both booking calendars, where 17:30 was blocked every single day
-- and 12:00 was blocked every day except Friday — on Friday the midday break
-- moves to 11:30 for Jumatan.
--
-- Stored per weekday rather than as a rule with an exception, because seven
-- rows saying when each day's break is can be read by whoever inherits this,
-- and "12:00 unless Friday" is a sentence that has to be reasoned about.
CREATE TABLE booking_breaks (
  id        TEXT    PRIMARY KEY,
  -- NULL is every day. The evening break does not move, so stating it seven
  -- times would be seven chances for six of them to stay right.
  weekday   INTEGER,
  starts_at INTEGER NOT NULL,
  ends_at   INTEGER NOT NULL,
  reason    TEXT    NOT NULL DEFAULT '',

  CONSTRAINT booking_breaks_weekday_is_a_weekday
    CHECK (weekday IS NULL OR weekday BETWEEN 0 AND 6),

  CONSTRAINT booking_breaks_run_forwards
    CHECK (starts_at >= 0 AND ends_at > starts_at AND ends_at <= 1440)
) STRICT;

-- One-off closures, as absolute instants: Idulfitri, a wedding the whole team
-- is shooting, a burst pipe. Distinct from booking_breaks because those recur
-- and these do not, and collapsing them would mean either a rule with a date
-- attached or a row per day forever.
CREATE TABLE booking_blackouts (
  id          TEXT    PRIMARY KEY,
  -- NULL closes every resource, which is what "closed today" means and is the
  -- entry an operator reaches for most.
  resource_id TEXT    REFERENCES booking_resources (id),
  starts_at   INTEGER NOT NULL,
  ends_at     INTEGER NOT NULL,
  reason      TEXT    NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,

  CONSTRAINT booking_blackouts_run_forwards
    CHECK (ends_at > starts_at)
) STRICT;

CREATE INDEX idx_booking_blackouts_window ON booking_blackouts (starts_at, ends_at);

CREATE TABLE bookings (
  id            TEXT    PRIMARY KEY,
  resource_id   TEXT    NOT NULL REFERENCES booking_resources (id),
  service_id    TEXT    NOT NULL REFERENCES booking_services (id),
  -- The customer's account, when the phone number resolves to one. Nullable and
  -- NO ACTION, not CASCADE: a booking is a thing that happened, and deleting an
  -- account should not quietly empty the calendar the studio works from.
  user_id       TEXT    REFERENCES users (id),

  starts_at     INTEGER NOT NULL,
  ends_at       INTEGER NOT NULL,
  headcount     INTEGER NOT NULL,

  -- Copied onto the booking rather than read through user_id. A booking is a
  -- record of what was agreed, and a customer who later corrects the spelling of
  -- their name has not changed who turned up last Tuesday.
  name          TEXT    NOT NULL,
  phone         TEXT    NOT NULL,
  email         TEXT    NOT NULL DEFAULT '',
  notes         TEXT    NOT NULL DEFAULT '',

  status        TEXT    NOT NULL DEFAULT 'confirmed',
  -- The event in the owner's Google Calendar, once it exists. NULL means the
  -- booking is confirmed here and has not been mirrored yet — see the package
  -- doc for why that is a retry and never a failed booking.
  gcal_event_id TEXT,

  created_at    INTEGER NOT NULL,
  cancelled_at  INTEGER,

  CONSTRAINT bookings_status_known
    CHECK (status IN ('confirmed', 'cancelled')),

  CONSTRAINT bookings_run_forwards
    CHECK (ends_at > starts_at),

  CONSTRAINT bookings_headcount_is_positive
    CHECK (headcount > 0),

  -- The two halves of a cancellation cannot disagree. A row marked cancelled
  -- with no time is one nobody can account for at the end of the month, and a
  -- confirmed row carrying a cancellation time is a slot the operator believes
  -- is free.
  CONSTRAINT bookings_cancelled_has_a_time
    CHECK ((status = 'cancelled') = (cancelled_at IS NOT NULL))
) STRICT;

-- What is occupied, one row per grid point a booking covers.
--
-- This is the constraint that actually prevents a double booking, and it is a
-- table rather than a unique index on bookings(resource_id, starts_at) because
-- sessions are not all one slot long. A three-hour shoot starting at 09:00 and a
-- one-hour one starting at 10:00 have different start times and overlap
-- completely; an index on the start instant accepts both. Writing a row per
-- half hour turns "these overlap" into "these collide", which is a thing a
-- unique constraint can see.
--
-- Checking for a clash in Go and then inserting loses to a second request that
-- arrives in between, and two people at one door holding confirmations for the
-- same room is a mistake that cannot be fixed afterwards. Same reasoning as
-- idx_loyalty_idempotency: state the invariant where concurrency cannot get
-- underneath it, and let the loser see a constraint violation. The rows for one
-- booking are inserted in its transaction, so a collision on any half hour
-- rolls back the whole booking.
--
-- Current state, not history: cancelling deletes these rows and leaves the
-- booking behind, so the slot is free again and the record that it was taken and
-- given back is still in bookings.
CREATE TABLE booking_slots (
  resource_id TEXT    NOT NULL REFERENCES booking_resources (id),
  starts_at   INTEGER NOT NULL,
  booking_id  TEXT    NOT NULL REFERENCES bookings (id),

  PRIMARY KEY (resource_id, starts_at)
) STRICT;

CREATE INDEX idx_booking_slots_booking ON booking_slots (booking_id);

-- The operator's day view, and the availability sweep, both read a time range
-- across every status.
CREATE INDEX idx_bookings_when ON bookings (starts_at);

-- A customer looking up or cancelling their own booking has only their phone
-- number, because that is all the booking form asked for.
CREATE INDEX idx_bookings_phone ON bookings (phone, starts_at DESC);

-- Google Calendar's answer to "when is this resource busy", cached.
--
-- A cache, and the distinction is the whole point: the owner blocks time by
-- hand in Google Calendar, so these ranges are real, but if the fetch fails the
-- right behaviour is to keep offering yesterday's answer rather than to show a
-- studio that is open as closed. Stale availability risks a clash the operator
-- can resolve with a phone call; unavailable availability loses the sale
-- outright.
--
-- No primary key. The set is replaced wholesale per resource on every poll, so
-- an individual range has no identity worth naming.
CREATE TABLE booking_calendar_busy (
  resource_id TEXT    NOT NULL REFERENCES booking_resources (id),
  starts_at   INTEGER NOT NULL,
  ends_at     INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_booking_calendar_busy_resource
  ON booking_calendar_busy (resource_id, starts_at);

-- What the last poll managed, so a failure is visible to an operator instead of
-- being inferred from a calendar that stopped changing. Kept beside the cache
-- rather than in it, because the rows above are deleted on every successful
-- poll and this has to outlive them.
CREATE TABLE booking_calendar_sync (
  resource_id TEXT    PRIMARY KEY REFERENCES booking_resources (id),
  -- Last poll that succeeded. Reading this against the current time is how the
  -- console can say "busy times are 40 minutes stale" rather than nothing.
  fetched_at  INTEGER NOT NULL DEFAULT 0,
  window_from INTEGER NOT NULL DEFAULT 0,
  window_to   INTEGER NOT NULL DEFAULT 0,
  -- Last error, cleared on success. Text and not a flag: "which calendar id"
  -- and "not shared with the service account" are the two failures worth
  -- telling apart, and both arrive as prose from Google.
  error       TEXT    NOT NULL DEFAULT ''
) STRICT;
