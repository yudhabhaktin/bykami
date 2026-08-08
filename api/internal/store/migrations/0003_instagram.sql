-- The Instagram mirror.
--
-- The studio's Instagram is where its customers already are, and the websites
-- are what search engines read. This table is the join between them: a copy of
-- the posts, held here so the sites can render them as their own HTML and serve
-- the pictures from their own domain.
--
-- Copied rather than linked, for a reason that is not preference. Instagram's
-- `media_url` is a signed CDN link that stops resolving within hours, so a site
-- that stored the URL would show working images on the day it deployed and
-- broken ones by the weekend. The bytes are the only durable part.

CREATE TABLE instagram_posts (
  -- Instagram's own media id. Stable for the life of the post, which is what
  -- makes "do we already have this one?" answerable without downloading it.
  id          TEXT    PRIMARY KEY,

  -- Parsed out of the permalink rather than stored as a whole URL, matching
  -- packages/content: the shortcode is the fact and the link is a rendering of
  -- it. `kind` is the one part the shortcode cannot tell you — Instagram serves
  -- a reel under /reel/ and a photo under /p/.
  shortcode   TEXT    NOT NULL,
  kind        TEXT    NOT NULL,
  permalink   TEXT    NOT NULL,

  -- The caption as posted. Empty is normal and not an error.
  caption     TEXT    NOT NULL DEFAULT '',

  -- The picture itself. Same reasoning as frames.png — see that migration and
  -- internal/frames: a BLOB keeps the bytes in the same backup and the same
  -- transaction as the row describing them, where a bucket would add a
  -- credential to rotate and a second thing that can be down on its own.
  --
  -- For a video post this is the poster frame, not the video. A grid on a
  -- marketing page wants a still; anyone who wants the clip has the permalink.
  media       BLOB    NOT NULL,
  media_type  TEXT    NOT NULL,
  sha256      TEXT    NOT NULL,

  -- Read out of the image, never typed in, for the same reason a frame's sheet
  -- size is read out of its PNG. The site needs both to reserve the space
  -- before the picture loads; a grid that reflows as images arrive is a layout
  -- shift, and layout shift is one of the things this whole exercise is
  -- supposed to protect.
  width       INTEGER NOT NULL,
  height      INTEGER NOT NULL,

  -- When it was posted, unix seconds. The display order.
  taken_at    INTEGER NOT NULL,
  -- When this row was last confirmed present in the feed.
  fetched_at  INTEGER NOT NULL,

  CONSTRAINT instagram_posts_kind_known
    CHECK (kind IN ('p', 'reel')),

  -- The shortcode is joined onto a URL by the sites. Anything outside the
  -- alphabet Instagram actually uses has no business in it.
  CONSTRAINT instagram_posts_shortcode_is_a_shortcode
    CHECK (shortcode <> '' AND shortcode NOT GLOB '*[^A-Za-z0-9_-]*'),

  CONSTRAINT instagram_posts_has_dimensions
    CHECK (width > 0 AND height > 0)
) STRICT;

-- The site's read: newest first.
CREATE INDEX idx_instagram_posts_recent ON instagram_posts (taken_at DESC);

-- The access token, which is a rotating credential and therefore cannot live
-- only in the environment file that seeded it.
--
-- This is the whole cost of self-hosting instead of renting a widget. Meta
-- issues a long-lived token good for 60 days and expects the holder to exchange
-- it for a fresh one before it lapses; the new token replaces the old. A
-- process that read the token from the environment and never wrote it back
-- would present a dead credential the first time it restarted after a refresh,
-- and the symptom — a feed that silently stopped updating — is one nobody
-- notices for weeks.
CREATE TABLE instagram_token (
  -- One row, enforced. There is one account.
  id           INTEGER PRIMARY KEY CHECK (id = 1),
  token        TEXT    NOT NULL,
  -- Unix seconds. Assumed at 60 days when a token is first seeded from the
  -- environment, then replaced by what Meta actually reports on the first
  -- successful refresh.
  expires_at   INTEGER NOT NULL,
  refreshed_at INTEGER NOT NULL
) STRICT;
