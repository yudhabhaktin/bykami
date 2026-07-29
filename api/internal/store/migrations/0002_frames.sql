-- The frame catalogue.
--
-- Frames are the shared asset a franchise consumes: one catalogue, every outlet
-- pulling from it. That is why they live here rather than on each booth PC,
-- where they would have to be copied by hand to every new outlet and would
-- diverge the first time somebody forgot.

CREATE TABLE frames (
  -- A slug derived from the name, and the directory name the booth writes the
  -- frame into. internal/frames restricts it to [a-z0-9-] before it gets here;
  -- the CHECK is the same rule stated where it cannot be skipped by a future
  -- caller that builds a row some other way.
  id          TEXT    PRIMARY KEY,
  name        TEXT    NOT NULL,
  -- The always-on label: wedding, ulang tahun, wisuda. Free text rather than a
  -- fixed list — unlike loyalty's verticals, nothing is settled by querying
  -- this, so a typo costs a mislabelled card and not a missing report row.
  group_name  TEXT    NOT NULL DEFAULT '',

  -- Both derived from the artwork by internal/frames, never typed in. Stored
  -- rather than recomputed because the booth needs them in the manifest and
  -- decoding every PNG on every poll would be work with a known answer.
  layout      TEXT    NOT NULL,
  cells       TEXT    NOT NULL,
  width       INTEGER NOT NULL,
  height      INTEGER NOT NULL,

  -- The artwork itself. See the package doc for why a BLOB and not a bucket:
  -- the bytes and the row describing them stay in one backup and one
  -- transaction, so a restore cannot produce a frame with no picture.
  png         BLOB    NOT NULL,
  -- Hex SHA-256 of png. The booth syncs on this: an unchanged hash means it
  -- already has the bytes, so a poll costs a small manifest rather than the
  -- whole catalogue.
  sha256      TEXT    NOT NULL,

  -- Off until somebody looks. Cells are inferred from a picture, and a person
  -- seeing the detected slots drawn over the frame is the check that inference
  -- was right; publishing on upload would put that check after the customer.
  published   INTEGER NOT NULL DEFAULT 0,

  -- The season, as unix seconds. NULL is unbounded at that end, so a frame with
  -- neither set runs whenever it is published.
  --
  -- Dates rather than a switch somebody flips. A Ramadan frame that has to be
  -- turned off by hand is one that is still on the booth in August, and the
  -- person who would notice is a customer.
  active_from  INTEGER,
  active_until INTEGER,

  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,

  -- This id is joined onto a filesystem path by the agent. Anything that is not
  -- a lowercase letter, digit or dash has no business in it, and there is no
  -- spelling of a parent directory that gets through this.
  CONSTRAINT frames_id_is_a_slug
    CHECK (id <> '' AND id NOT GLOB '*[^a-z0-9-]*'),

  CONSTRAINT frames_layout_known
    CHECK (layout IN ('4r', 'strip2x6', '6x8')),

  -- A window that closes before it opens is a frame that can never appear, and
  -- the symptom is an operator certain they published something.
  CONSTRAINT frames_season_runs_forwards
    CHECK (active_from IS NULL OR active_until IS NULL OR active_from < active_until)
) STRICT;

-- The booth's poll: published, in season, ordered for display. Covers the
-- filter so the catalogue is not scanned every few minutes on a 1 GB box.
CREATE INDEX idx_frames_live ON frames (published, active_from, active_until);
