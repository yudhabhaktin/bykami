package instagram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// seedToken writes the environment's token if there is not one already.
//
// Once only, and that is the point. After the first refresh the database holds
// a token the environment file has never seen, and overwriting it on every
// restart would hand back a credential Meta has already superseded — a service
// that works until it is restarted, which is the worst shape a bug can take.
//
// Replacing the seed by hand therefore needs the row cleared too, and the
// operator note in cmd/bykami says so.
func (c *Cache) seedToken(ctx context.Context, seed string) error {
	now := time.Now().UTC()
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO instagram_token (id, token, expires_at, refreshed_at)
		VALUES (1, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`,
		seed, now.Add(seedLife).Unix(), now.Unix())
	if err != nil {
		return fmt.Errorf("instagram: seed token: %w", err)
	}
	return nil
}

// token returns the live credential and when it expires.
func (c *Cache) token(ctx context.Context) (string, time.Time, error) {
	var tok string
	var expires int64
	err := c.db.QueryRowContext(ctx,
		`SELECT token, expires_at FROM instagram_token WHERE id = 1`).Scan(&tok, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, errors.New("instagram: no token stored")
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("instagram: read token: %w", err)
	}
	return tok, time.Unix(expires, 0).UTC(), nil
}

// putToken replaces the credential with a freshly refreshed one.
func (c *Cache) putToken(ctx context.Context, tok string, expires time.Time) error {
	_, err := c.db.ExecContext(ctx, `
		UPDATE instagram_token SET token = ?, expires_at = ?, refreshed_at = ? WHERE id = 1`,
		tok, expires.Unix(), time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("instagram: store token: %w", err)
	}
	return nil
}

func (c *Cache) has(ctx context.Context, id string) (bool, error) {
	var one int
	err := c.db.QueryRowContext(ctx, `SELECT 1 FROM instagram_posts WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("instagram: lookup: %w", err)
	}
	return true, nil
}

// touch updates what can change on a post that is already mirrored.
func (c *Cache) touch(ctx context.Context, id, caption, permalink string) error {
	_, err := c.db.ExecContext(ctx, `
		UPDATE instagram_posts SET caption = ?, permalink = ?, fetched_at = ? WHERE id = ?`,
		caption, permalink, time.Now().UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("instagram: touch: %w", err)
	}
	return nil
}

// put stores a post and its picture.
func (c *Cache) put(ctx context.Context, p Post, media []byte) error {
	now := time.Now().UTC().Unix()
	_, err := c.db.ExecContext(ctx, `
		INSERT INTO instagram_posts
			(id, shortcode, kind, permalink, caption, media, media_type, sha256,
			 width, height, taken_at, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			caption = excluded.caption,
			permalink = excluded.permalink,
			fetched_at = excluded.fetched_at`,
		p.ID, p.Shortcode, p.Kind, p.Permalink, p.Caption, media, p.MediaType, p.SHA256,
		p.Width, p.Height, p.TakenAt.Unix(), now)
	if err != nil {
		return fmt.Errorf("instagram: store post: %w", err)
	}
	return nil
}

// prune removes posts the feed no longer lists, and reports how many went.
//
// Called only after a poll that returned a feed — see Sync. An empty `keep` is
// therefore a real answer, meaning the account has no posts, and not the
// silence of a request that failed.
func (c *Cache) prune(ctx context.Context, keep map[string]bool) (int, error) {
	if len(keep) == 0 {
		res, err := c.db.ExecContext(ctx, `DELETE FROM instagram_posts`)
		if err != nil {
			return 0, fmt.Errorf("instagram: prune: %w", err)
		}
		n, _ := res.RowsAffected()
		return int(n), nil
	}

	ids := make([]any, 0, len(keep))
	for id := range keep {
		ids = append(ids, id)
	}
	q := `DELETE FROM instagram_posts WHERE id NOT IN (?` + strings.Repeat(",?", len(ids)-1) + `)`

	res, err := c.db.ExecContext(ctx, q, ids...)
	if err != nil {
		return 0, fmt.Errorf("instagram: prune: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
