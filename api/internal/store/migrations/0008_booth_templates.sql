-- What a booth is actually offering, as reported by the booth.
--
-- The catalogue in 0002 is what an operator has uploaded. It is not what a
-- customer sees. A booth's frame set is the designs compiled into the agent
-- binary, PLUS the published catalogue, PLUS anything in its local -templates
-- directory — added together, never chosen between (see the README in
-- agent/internal/compose/templates). So the console could show a catalogue of
-- four while the booth offered eleven, and nothing anywhere said so.
--
-- The two binaries are two Go modules that deliberately share no code, so the
-- API cannot read the agent's built-ins by importing them; and even if it
-- could, the answer would be this repository's built-ins rather than the ones
-- on a booth that has not been updated since June. The only honest source is
-- the booth, so the booth says.
--
-- Everything here is a cache of somebody else's truth. It is derived, it can be
-- deleted, and a booth that has never reported simply has no rows — which the
-- console renders as "belum lapor" rather than as "no frames".

CREATE TABLE booth_reports (
  -- The agent's -outlet, which is the only name a booth has. Restricted to a
  -- slug where it arrives from the network rather than trusted, for the same
  -- reason frames.id is.
  outlet      TEXT    PRIMARY KEY,
  reported_at INTEGER NOT NULL,

  CONSTRAINT booth_reports_outlet_is_a_slug
    CHECK (outlet <> '' AND outlet NOT GLOB '*[^a-z0-9-]*')
) STRICT;

CREATE TABLE booth_designs (
  outlet    TEXT    NOT NULL REFERENCES booth_reports(outlet) ON DELETE CASCADE,
  -- The template id the kiosk sends back when a customer picks it. Not a
  -- foreign key to frames: most of these are built into the agent and have no
  -- catalogue row at all, and that is the whole point of the table.
  id        TEXT    NOT NULL,
  name      TEXT    NOT NULL,
  layout    TEXT    NOT NULL,
  cells     TEXT    NOT NULL,

  -- Hex SHA-256 of the overlay artwork, or '' for a design that draws none.
  -- The join to booth_artwork, and the reason a fleet of booths running the
  -- same seven built-ins stores those bytes once.
  sha256    TEXT    NOT NULL,

  -- Where it sits in the list the kiosk shows, so the console can display the
  -- booth's own order rather than inventing one.
  position  INTEGER NOT NULL,

  PRIMARY KEY (outlet, id),

  CONSTRAINT booth_designs_layout_known
    CHECK (layout IN ('4r', 'strip2x6', '6x8'))
) STRICT;

-- Artwork keyed by its own hash rather than by (outlet, id).
--
-- Content addressing, which is already the sync protocol in the other
-- direction: agent/internal/framesync decides whether to download by comparing
-- hashes, and this decides whether to upload the same way. It means a booth
-- uploads a design once however many booths already have it, and re-uploads
-- nothing at all on the poll after that.
CREATE TABLE booth_artwork (
  sha256    TEXT    PRIMARY KEY,
  png       BLOB    NOT NULL,
  stored_at INTEGER NOT NULL
) STRICT;
