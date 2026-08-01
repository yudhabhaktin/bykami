-- The moving version of a printed sheet.
--
-- One animation of the whole frame: every cell playing its own clip at once,
-- inside the artwork the designer drew. The rows in `clips` are the same
-- seconds shown one face at a time; this is the thing the customer is actually
-- holding, moving. A six-slot design animates six, a three-slot design three —
-- the count is the template's, never this table's.
--
-- # Why the render inputs are copied here
--
-- print_jobs records what went to the printer: a layout, a composed file, a
-- media cost. It does not record what the sheet was made OF, and it should not
-- — it is the media ledger, and its CHECK constraints are about paper.
--
-- Rebuilding a sheet as an animation needs three things the job does not have:
-- the template, the filter, and the frames in cell order. They exist for one
-- moment, inside the print request, and are dropped when it returns. So they
-- are written down here at that moment. Recovering them afterwards is not
-- possible from either side — the job knows only a path, and the composed JPEG
-- has been flattened past the point where the cells can be told apart.
--
-- # Why a row rather than a queue in memory
--
-- The render is a minute of background CPU on a booth whose own updater
-- restarts it on a timer. A queue that lived in the process would lose exactly
-- the sessions that were mid-print when the new binary landed, and the customer
-- who lost one is holding the printed sheet asking where the moving one is.
CREATE TABLE sheet_clips (
  id          TEXT    PRIMARY KEY,
  -- UNIQUE because a print job is one sheet, so it has one animation. It also
  -- makes enqueueing idempotent, which matters because the print handler
  -- composes before it queues and may be retried by a client that never saw
  -- the response.
  job_id      TEXT    NOT NULL UNIQUE REFERENCES print_jobs (id),
  -- Copied rather than joined through the job, for the same reason clips
  -- copies it: the download page lists a session's animations without reaching
  -- through another table to do it.
  session_id  TEXT    NOT NULL REFERENCES sessions (id),

  template_id TEXT    NOT NULL,
  filter      TEXT    NOT NULL,
  -- A JSON array of photo ids. A child table would be the ordinary shape and
  -- is the wrong one here: the order IS the data — cell 1 takes photo_ids[0] —
  -- and a list whose order is load-bearing is one value, not five rows that a
  -- forgotten ORDER BY can quietly shuffle into somebody else's face.
  photo_ids   TEXT    NOT NULL,

  queued_at   INTEGER NOT NULL,

  -- The delivered animation, relative to the store root, written by the render
  -- worker. NULL until it has been built.
  --
  -- There is no purged_at beside it, unlike clips. The file is written under
  -- sheets/, so purge's file-age sweep of that tree takes it with the JPEG it
  -- moves — the same rule, because it is the same faces. What that leaves is a
  -- row pointing at a deleted file, so the download page stats before it
  -- offers, exactly as it already does for the sheet itself.
  gif_path    TEXT,
  gif_at      INTEGER,

  -- Set when the sources are gone for good and the render will never succeed:
  -- the frames reached retention before the worker reached the job. Without it
  -- the queue keeps this row forever and every restart re-attempts a render
  -- whose inputs were deleted days ago.
  abandoned_at INTEGER,

  -- A path with no time, or a time with no path, is a half-written render that
  -- some later query will trust. The two are one fact.
  CONSTRAINT sheet_clip_gif_is_whole
    CHECK ((gif_path IS NULL) = (gif_at IS NULL))
) STRICT;

-- Drives the render worker's queue without scanning the table.
CREATE INDEX idx_sheet_clips_unrendered
  ON sheet_clips (queued_at) WHERE gif_path IS NULL AND abandoned_at IS NULL;

CREATE INDEX idx_sheet_clips_session ON sheet_clips (session_id);
