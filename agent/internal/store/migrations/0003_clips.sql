-- The moving version of a frame.
--
-- A clip is the few seconds of camera the booth was already showing before the
-- shutter fired, kept as a run of small JPEGs and rendered to an animated GIF
-- for the download page. It is the same instant as the photo beside it, which
-- is why it hangs off photo_id rather than standing on its own.
--
-- # Why this is not the photos table
--
-- Every row in photos is a frame the customer can choose, print and be charged
-- a take for: the review screen lists them, and session.MayFire counts them
-- against take_limit. A five-second clip is fifty frames. Ingesting them as
-- photos would fill the strip picker with near-identical thumbnails and exhaust
-- a fifteen-take session on the second shot — so the motion frames are kept
-- here, where nothing counts them and nothing offers them to be printed.
--
-- They also never pass through ingest: it is content-addressed, and a burst of
-- fifty frames of a person holding still contains byte-identical neighbours
-- that the photos table would reject as duplicates.
CREATE TABLE clips (
  id         TEXT    PRIMARY KEY,
  -- UNIQUE because a photo has one moment, not several. It also makes the
  -- upload idempotent: a retried burst is a constraint violation to swallow
  -- rather than a second copy of the same five seconds on disk.
  photo_id   TEXT    NOT NULL UNIQUE REFERENCES photos (id),
  -- Copied from the photo rather than joined for it, so the gallery can list a
  -- session's clips without reaching through photos, and so an orphan frame's
  -- clip has somewhere to be filed.
  session_id TEXT    REFERENCES sessions (id),

  -- The directory holding this clip's frames, relative to the store root. A
  -- directory of its own per clip, because deleting one is then a single
  -- RemoveAll rather than reconstructing fifty filenames from a count — and
  -- retention here has to be obviously correct.
  dir        TEXT    NOT NULL,
  frames     INTEGER NOT NULL,

  -- The photo's capture time, not the upload's. The burst is posted after the
  -- shutter returns so it never delays the next countdown, which means its own
  -- arrival time is a few hundred milliseconds of network noise.
  captured_at INTEGER NOT NULL,

  -- The delivered animation, written by the render worker. NULL means it has
  -- not been built yet, which is the normal state for the first seconds of its
  -- life and is what the worker sweeps for.
  gif_path   TEXT,
  gif_at     INTEGER,

  -- Set when the frames are deleted at retention, exactly as photos.purged_at
  -- is. The row outlives the files for the same reason: it is how the render
  -- worker knows not to keep trying to build a GIF from bytes that are gone.
  purged_at  INTEGER,

  CONSTRAINT clip_frames_positive
    CHECK (frames > 0),

  -- A path with no time, or a time with no path, is a half-written render that
  -- some later query will trust. The two are one fact.
  CONSTRAINT clip_gif_is_whole
    CHECK ((gif_path IS NULL) = (gif_at IS NULL))
) STRICT;

-- Drives the render worker's queue without scanning the table.
CREATE INDEX idx_clips_unrendered
  ON clips (captured_at) WHERE gif_path IS NULL AND purged_at IS NULL;

CREATE INDEX idx_clips_session ON clips (session_id);
