package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/instagram"
)

// The social mirror, as the sites read it.
//
// Public and unauthenticated, which is the right answer rather than a shortcut:
// every byte here is a copy of something already public on instagram.com, and
// the pictures are fetched by the browsers of people reading a marketing page.
// A token would protect nothing and would have to be embedded in a static site
// to be useful, which is not protection at all.
//
// Read-only, and there is no write surface anywhere — the only thing that puts
// rows in this table is internal/instagram polling Meta.

type socialPost struct {
	ID        string `json:"id"`
	Shortcode string `json:"shortcode"`
	// Kind is "p" or "reel", matching the enum in packages/content so the site
	// can rebuild a permalink without parsing one.
	Kind      string `json:"kind"`
	Permalink string `json:"permalink"`
	Caption   string `json:"caption,omitempty"`
	// Media is the path this post's picture is served from, relative to the
	// API. The site turns it into an absolute URL against whatever host it was
	// built for; hardcoding one here would bake a hostname into the database's
	// output and make a staging build serve production pictures.
	Media  string `json:"media"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	// SHA256 lets a build skip a download it already has, the same way the
	// booth skips frame artwork.
	SHA256  string    `json:"sha256"`
	TakenAt time.Time `json:"taken_at"`
}

type socialFeed struct {
	Account string       `json:"account"`
	Posts   []socialPost `json:"posts"`
}

// instagramFeed serves the mirrored posts, newest first.
//
// An empty list is a 200 and not a 404. "Nothing mirrored yet" is a normal
// state — a deployment with no token, a first poll that has not run — and the
// site's job in that case is to render no section at all, which is easier to do
// from an empty array than from an error.
func (a *API) instagramFeed(w http.ResponseWriter, r *http.Request) {
	posts, err := a.instagram.Posts(r.Context())
	if err != nil {
		a.internal(w, "instagram feed", err)
		return
	}

	out := socialFeed{Account: a.instagramAccount, Posts: make([]socialPost, 0, len(posts))}
	for _, p := range posts {
		out.Posts = append(out.Posts, socialPost{
			ID: p.ID, Shortcode: p.Shortcode, Kind: p.Kind, Permalink: p.Permalink,
			Caption: p.Caption, Media: "/v1/social/instagram/" + p.ID,
			Width: p.Width, Height: p.Height, SHA256: p.SHA256, TakenAt: p.TakenAt,
		})
	}

	// Short, because this is what a nightly site build reads and a stale
	// manifest is a build that misses a day. The pictures below are the
	// expensive part and they are cached hard.
	w.Header().Set("Cache-Control", "public, max-age=300")
	a.write(w, http.StatusOK, out)
}

// instagramMedia serves one mirrored picture.
func (a *API) instagramMedia(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if i := strings.LastIndexByte(id, '.'); i > 0 {
		// Tolerated so a build can save the file under a name with an
		// extension and fetch it back by the same one.
		id = id[:i]
	}

	body, mediaType, sum, err := a.instagram.Media(r.Context(), id)
	if errors.Is(err, instagram.ErrNoPost) {
		a.fail(w, http.StatusNotFound, "no such post")
		return
	}
	if err != nil {
		a.internal(w, "instagram media", err)
		return
	}

	h := w.Header()
	h.Set("Content-Type", mediaType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("ETag", `"`+sum+`"`)
	// A post's picture never changes under the same id — Instagram issues a new
	// id for a new upload — so this can be cached for as long as anyone is
	// willing to hold it. When a post is withdrawn the row goes and this starts
	// answering 404, which is the only invalidation that matters.
	h.Set("Cache-Control", "public, max-age=31536000, immutable")

	if r.Header.Get("If-None-Match") == `"`+sum+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if _, err := w.Write(body); err != nil {
		a.log.Error("httpapi: write instagram media", "err", err, "post", id)
	}
}
