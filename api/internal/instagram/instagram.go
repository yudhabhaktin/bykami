// Package instagram mirrors the studio's Instagram feed into the database.
//
// The sites want to show what the studio has been posting. The rented way to do
// that is a third-party widget; this is the other way, and the trade is worth
// stating plainly.
//
// A widget is an iframe. Its contents belong to instagram.com, so none of it is
// indexable as the page's content, and the whole grid arrives as third-party
// JavaScript on pages that otherwise ship almost none. Mirroring costs this
// package and one rotating credential, and buys back HTML the crawler reads and
// pictures served from bykami's own domain.
//
// # It is a cache, not a dependency
//
// Nothing here is on a request path. The worker polls, writes rows, and stops;
// the sites read what is stored. A poll that fails — Instagram down, the token
// finally dead, a shop internet connection having a bad afternoon — leaves the
// last good copy exactly where it was, because the alternative is a marketing
// page that goes blank when someone else's API does. Every failure is logged
// and retried on the next tick, and the only thing that removes a post is a
// successful poll that no longer lists it.
//
// # Why the bytes are copied
//
// Instagram's media_url is a signed CDN link with hours of life in it. Storing
// the URL would produce a page that works the day it deploys and breaks by the
// weekend, with nothing in the logs to say why. The picture is the only durable
// part, so the picture is what gets saved.
//
// # The token is the whole cost of self-hosting
//
// Meta issues a long-lived token good for 60 days and expects it to be
// exchanged for a fresh one before it lapses. That exchange is the one piece of
// unattended machinery here, and the one thing that will eventually break: see
// refreshWindow, and the note in the 0003 migration about why the current token
// lives in the database rather than in the environment file that seeded it.
package instagram

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // registers the decoder DecodeConfig needs
	_ "image/png"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBase is Meta's host for the Instagram API with Instagram Login.
//
// Versioned, and that version is the one knob most likely to need turning:
// Meta retires a Graph API version roughly every two years, and when this one
// goes the symptom is every call failing at once. Overridable for that reason
// as much as for tests.
const DefaultBase = "https://graph.instagram.com/v23.0"

// DefaultInterval between polls.
//
// Hourly because a studio posts a few times a week, and a page that is at worst
// an hour behind its Instagram is indistinguishable from one that is current.
// It is also 24 calls a day against a limit of 200 an hour, so the quota is
// never the thing that breaks.
const DefaultInterval = time.Hour

// DefaultLimit is how many posts to keep.
//
// A grid, not an archive. Twelve is three rows of four on a desktop and enough
// to look alive; the account's older posts are one click away on Instagram,
// which is better at showing them than this ever will be.
const DefaultLimit = 12

// maxMedia bounds one download. An Instagram photo is a few hundred kilobytes,
// so anything approaching this is not a photo — it is a captive portal or a
// proxy error page, and it should be refused rather than stored as one.
const maxMedia = 8 << 20

// refreshWindow is how long before expiry the token is exchanged.
//
// Ten days, against a 60-day life. Long enough that a box switched off for a
// fortnight still wakes up in time to save itself, and long enough that ten
// consecutive daily failures would have to pass unnoticed before anything is
// actually lost. Meta refuses to refresh a token less than 24 hours old, so the
// window cannot sensibly be much tighter at the other end either.
const refreshWindow = 10 * 24 * time.Hour

// seedLife is the expiry assumed for a token pasted into the environment file.
//
// Meta issues long-lived tokens with 60 days on them but does not say so
// anywhere the token itself can be read, so the first refresh is what replaces
// this guess with the truth. Guessing high would be the dangerous direction;
// this errs by refreshing sooner than strictly needed.
const seedLife = 60 * 24 * time.Hour

// Post is one mirrored post.
type Post struct {
	ID        string
	Shortcode string
	// Kind is "p" or "reel", matching the path Instagram serves it under and
	// the enum in packages/content.
	Kind      string
	Permalink string
	Caption   string
	MediaType string
	SHA256    string
	Width     int
	Height    int
	TakenAt   time.Time
}

// Cache is the stored copy: everything the sites read, and the only thing they
// read. Constructed whether or not polling is configured, because a mirror with
// no worker behind it is still a mirror — a token that finally died should cost
// the next update, not the posts already saved.
type Cache struct {
	db *sql.DB
}

func New(db *sql.DB) *Cache { return &Cache{db: db} }

// Posts returns the mirror, newest first.
func (c *Cache) Posts(ctx context.Context) ([]Post, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT id, shortcode, kind, permalink, caption, media_type, sha256, width, height, taken_at
		FROM instagram_posts
		ORDER BY taken_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("instagram: list: %w", err)
	}
	defer rows.Close()

	var out []Post
	for rows.Next() {
		var p Post
		var taken int64
		if err := rows.Scan(&p.ID, &p.Shortcode, &p.Kind, &p.Permalink, &p.Caption,
			&p.MediaType, &p.SHA256, &p.Width, &p.Height, &taken); err != nil {
			return nil, fmt.Errorf("instagram: scan: %w", err)
		}
		p.TakenAt = time.Unix(taken, 0).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// ErrNoPost means nothing is mirrored under that id.
var ErrNoPost = errors.New("instagram: no such post")

// Media returns one post's bytes, its content type, and its hash.
func (c *Cache) Media(ctx context.Context, id string) ([]byte, string, string, error) {
	var body []byte
	var mediaType, sum string
	err := c.db.QueryRowContext(ctx,
		`SELECT media, media_type, sha256 FROM instagram_posts WHERE id = ?`, id).
		Scan(&body, &mediaType, &sum)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", "", ErrNoPost
	}
	if err != nil {
		return nil, "", "", fmt.Errorf("instagram: media: %w", err)
	}
	return body, mediaType, sum, nil
}

// Worker polls Instagram and keeps the Cache in step.
type Worker struct {
	cache    *Cache
	base     string
	interval time.Duration
	limit    int
	log      *slog.Logger
	client   *http.Client
}

// NewWorker returns a worker, or nil if mirroring is not configured.
//
// Nil rather than an error, matching framesync: no token is the normal state of
// a deployment nobody has connected to Instagram, and it should start and serve
// the rest of the platform. seed is the token from the environment file; it is
// written to the database once and thereafter the database is the authority,
// because the token in the environment stops being the live one the first time
// it is refreshed.
func NewWorker(ctx context.Context, cache *Cache, seed, base string, interval time.Duration, limit int, log *slog.Logger) (*Worker, error) {
	if seed == "" {
		return nil, nil
	}
	if base == "" {
		base = DefaultBase
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	if limit <= 0 {
		limit = DefaultLimit
	}

	if err := cache.seedToken(ctx, seed); err != nil {
		return nil, err
	}

	return &Worker{
		cache: cache, base: strings.TrimSuffix(base, "/"),
		interval: interval, limit: limit, log: log,
		// Bounded. A stalled poll must not hold a goroutine until the process
		// restarts, and this one runs on a box with a gigabyte to its name.
		client: &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// Run polls until ctx is cancelled, starting immediately: a process that has
// just started has most likely been down for a while.
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTicker(w.interval)
	defer t.Stop()

	for {
		if err := w.Sync(ctx); err != nil && !errors.Is(err, context.Canceled) {
			// Logged and dropped. The sites keep the posts already mirrored;
			// see the package doc for why this is never fatal.
			w.log.Warn("instagram sync failed; keeping the posts already mirrored", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Sync refreshes the token if it is due, fetches the feed, and stores what changed.
func (w *Worker) Sync(ctx context.Context) error {
	token, expires, err := w.cache.token(ctx)
	if err != nil {
		return err
	}

	if time.Until(expires) < refreshWindow {
		if fresh, life, err := w.refresh(ctx, token); err != nil {
			// Not fatal, and deliberately not a reason to skip the poll: the
			// current token is valid until it is not, and a refresh that failed
			// today has ten days of tomorrows to succeed in.
			w.log.Warn("could not refresh the Instagram token; it still has time left",
				"err", err, "expires", expires.Format(time.RFC3339))
		} else {
			if err := w.cache.putToken(ctx, fresh, life); err != nil {
				return err
			}
			token = fresh
			w.log.Info("Instagram token refreshed", "expires", life.Format(time.RFC3339))
		}
	}

	items, err := w.feed(ctx, token)
	if err != nil {
		return err
	}

	seen := make(map[string]bool, len(items))
	added := 0
	for _, it := range items {
		shortcode, kind, err := parsePermalink(it.Permalink)
		if err != nil {
			w.log.Warn("skipping a post whose permalink does not parse", "id", it.ID, "err", err)
			continue
		}
		seen[it.ID] = true

		// The caption can be edited after posting; the picture cannot be
		// swapped under the same id. So metadata is refreshed every poll and
		// the bytes are fetched exactly once, which is what keeps the common
		// poll to a single small request.
		if has, err := w.cache.has(ctx, it.ID); err != nil {
			return err
		} else if has {
			if err := w.cache.touch(ctx, it.ID, it.Caption, it.Permalink); err != nil {
				w.log.Warn("could not update a mirrored post", "id", it.ID, "err", err)
			}
			continue
		}

		src := it.MediaURL
		if it.MediaType == "VIDEO" {
			// A grid wants a still. The clip is one click away on the permalink
			// and Instagram is better at playing it than this is.
			src = it.ThumbnailURL
		}
		if src == "" {
			w.log.Warn("skipping a post with no image to fetch", "id", it.ID, "type", it.MediaType)
			continue
		}

		body, mediaType, err := w.download(ctx, src)
		if err != nil {
			// One bad post does not abandon the rest.
			w.log.Warn("could not mirror a post", "id", it.ID, "err", err)
			continue
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
		if err != nil {
			w.log.Warn("refusing a post whose bytes are not an image", "id", it.ID, "err", err)
			continue
		}

		sum := sha256.Sum256(body)
		if err := w.cache.put(ctx, Post{
			ID: it.ID, Shortcode: shortcode, Kind: kind, Permalink: it.Permalink,
			Caption: it.Caption, MediaType: mediaType, SHA256: hex.EncodeToString(sum[:]),
			Width: cfg.Width, Height: cfg.Height, TakenAt: it.taken,
		}, body); err != nil {
			w.log.Warn("could not store a post", "id", it.ID, "err", err)
			continue
		}
		added++
	}

	// Only after a poll that actually returned a feed. A fetch that failed
	// tells you nothing about what was withdrawn, and treating it as an empty
	// feed would empty the sites.
	removed, err := w.cache.prune(ctx, seen)
	if err != nil {
		w.log.Warn("could not remove withdrawn posts", "err", err)
	}

	if added == 0 && removed == 0 {
		return nil
	}
	w.log.Info("instagram mirror updated", "added", added, "removed", removed, "total", len(seen))
	return nil
}

// item is one entry of the /me/media response.
type item struct {
	ID           string `json:"id"`
	Caption      string `json:"caption"`
	MediaType    string `json:"media_type"`
	MediaURL     string `json:"media_url"`
	ThumbnailURL string `json:"thumbnail_url"`
	Permalink    string `json:"permalink"`
	Timestamp    string `json:"timestamp"`

	taken time.Time
}

// graphError is Meta's error envelope, which arrives with a 400 rather than in
// the shape of the success response.
type graphError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    int    `json:"code"`
	} `json:"error"`
}

func (w *Worker) feed(ctx context.Context, token string) ([]item, error) {
	q := url.Values{}
	q.Set("fields", "id,caption,media_type,media_url,thumbnail_url,permalink,timestamp")
	q.Set("limit", fmt.Sprint(w.limit))
	q.Set("access_token", token)

	body, err := w.get(ctx, w.base+"/me/media?"+q.Encode())
	if err != nil {
		return nil, err
	}

	var payload struct {
		Data []item `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("instagram: decode feed: %w", err)
	}

	out := make([]item, 0, len(payload.Data))
	for _, it := range payload.Data {
		// Meta's format, which is ISO 8601 with no colon in the offset.
		t, err := time.Parse("2006-01-02T15:04:05-0700", it.Timestamp)
		if err != nil {
			w.log.Warn("skipping a post with an unparseable timestamp", "id", it.ID, "stamp", it.Timestamp)
			continue
		}
		it.taken = t.UTC()
		out = append(out, it)
		if len(out) == w.limit {
			break
		}
	}
	return out, nil
}

// refresh exchanges the current token for a fresh 60 days, returning the new
// token and when it expires.
func (w *Worker) refresh(ctx context.Context, token string) (string, time.Time, error) {
	q := url.Values{}
	q.Set("grant_type", "ig_refresh_token")
	q.Set("access_token", token)

	body, err := w.get(ctx, w.base+"/refresh_access_token?"+q.Encode())
	if err != nil {
		return "", time.Time{}, err
	}

	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", time.Time{}, fmt.Errorf("instagram: decode refresh: %w", err)
	}
	if payload.AccessToken == "" || payload.ExpiresIn <= 0 {
		return "", time.Time{}, errors.New("instagram: refresh returned no usable token")
	}
	return payload.AccessToken, time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second).UTC(), nil
}

func (w *Worker) get(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	res, err := w.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("instagram: request: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("instagram: read: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		// Meta puts the useful part in the body, and "400 Bad Request" on its
		// own has sent people looking in the wrong place for an expired token.
		var ge graphError
		if json.Unmarshal(body, &ge) == nil && ge.Error.Message != "" {
			return nil, fmt.Errorf("instagram: %s (%s, code %d)", ge.Error.Message, ge.Error.Type, ge.Error.Code)
		}
		return nil, fmt.Errorf("instagram: %s", res.Status)
	}
	return body, nil
}

// download fetches one picture, returning the bytes and the content type.
func (w *Worker) download(ctx context.Context, src string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, "", err
	}
	res, err := w.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("instagram: fetch media: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("instagram: fetch media: %s", res.Status)
	}

	mediaType := res.Header.Get("Content-Type")
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = strings.TrimSpace(mediaType[:i])
	}
	switch mediaType {
	case "image/jpeg", "image/png":
	default:
		return nil, "", fmt.Errorf("instagram: media is %q, not a picture", mediaType)
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, maxMedia+1))
	if err != nil {
		return nil, "", fmt.Errorf("instagram: read media: %w", err)
	}
	if len(body) > maxMedia {
		return nil, "", errors.New("instagram: media is larger than a photograph has any reason to be")
	}
	return body, mediaType, nil
}

// parsePermalink pulls the shortcode and kind out of a post URL.
func parsePermalink(raw string) (shortcode, kind string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("instagram: %q is not a post permalink", raw)
	}
	kind, shortcode = parts[0], parts[1]
	if kind != "p" && kind != "reel" {
		return "", "", fmt.Errorf("instagram: unknown permalink kind %q", kind)
	}
	if shortcode == "" || strings.ContainsAny(shortcode, "./\\") {
		return "", "", fmt.Errorf("instagram: %q is not a shortcode", shortcode)
	}
	return shortcode, kind, nil
}
