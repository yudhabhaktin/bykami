package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/frames"
)

// The booth sync surface.
//
// A booth is not a user. It has no phone, cannot receive a one-time code, and
// runs unattended in a shop — so it authenticates with a shared secret from its
// environment file rather than through identity. That is a different trust
// level and it gets a different, deliberately tiny, surface: two read-only
// routes that serve published frame artwork and nothing else.
//
// One secret for every booth rather than one each. With a single outlet, per-
// booth tokens would be a table, an enrolment flow and a revocation story built
// for a fleet that does not exist; what they would buy is the ability to revoke
// one booth without touching the others. When there is a second outlet that
// becomes worth having — and the note in agent/README.md says so.

// boothManifest is what a booth polls for. Deliberately not the artwork: a
// booth checks in every few minutes and almost always finds nothing changed, so
// the common case must cost a few hundred bytes rather than a catalogue of
// PNGs.
type boothManifest struct {
	// ServerTime lets the booth log a clock difference. Seasons are evaluated
	// here, on the server, so a booth with a wrong clock still gets the right
	// frames — but a booth that knows its clock is wrong can say so.
	ServerTime time.Time    `json:"server_time"`
	Frames     []boothFrame `json:"frames"`
}

type boothFrame struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	Group  string        `json:"group,omitempty"`
	Layout string        `json:"layout"`
	Cells  []frames.Cell `json:"cells"`
	// SHA256 is how the booth decides whether to download. Same hash, same
	// bytes, no request.
	SHA256 string `json:"sha256"`
}

// boothFrames serves the manifest of everything published and in season.
//
// Seasons are resolved against the server's clock rather than sent as date
// windows for the booth to evaluate. A booth PC's clock is the least trusted
// clock in the system — it is a shop computer that may have been off for a week
// — and a Lebaran frame appearing a day early because of it is exactly the
// failure the date window existed to prevent.
func (a *API) boothFrames(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	live, err := a.frames.Live(r.Context(), now)
	if err != nil {
		a.internal(w, "booth frames", err)
		return
	}

	out := boothManifest{ServerTime: now, Frames: make([]boothFrame, 0, len(live))}
	for _, f := range live {
		out.Frames = append(out.Frames, boothFrame{
			ID: f.ID, Name: f.Name, Group: f.Group,
			Layout: string(f.Layout), Cells: f.Cells, SHA256: f.SHA256,
		})
	}
	a.write(w, http.StatusOK, out)
}

// boothArtwork serves one frame's PNG.
//
// Only published frames, and the check is here rather than left to the manifest
// being the only way anyone learns an id. A draft is artwork an operator has
// not approved; that it is hard to guess is not a reason to serve it.
func (a *API) boothArtwork(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.PathValue("id"), ".png")

	f, err := a.frames.Get(r.Context(), id)
	if errors.Is(err, frames.ErrNoFrame) {
		a.fail(w, http.StatusNotFound, "no such frame")
		return
	}
	if err != nil {
		a.internal(w, "booth artwork", err)
		return
	}
	if !f.Live(time.Now().UTC()) {
		a.fail(w, http.StatusNotFound, "no such frame")
		return
	}

	art, sum, err := a.frames.Artwork(r.Context(), id)
	if err != nil {
		a.internal(w, "booth artwork bytes", err)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("ETag", `"`+sum+`"`)
	if r.Header.Get("If-None-Match") == `"`+sum+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if _, err := w.Write(art); err != nil {
		a.log.Error("httpapi: write booth artwork", "err", err, "frame", id)
	}
}

// booth admits a request carrying the booth token.
//
// Constant-time comparison of a hash rather than of the tokens themselves: the
// hash is fixed width, so the comparison cannot leak the secret's length, and
// subtle.ConstantTimeCompare on differing lengths returns early anyway.
func (a *API) booth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(a.boothToken) == 0 {
			// No token configured means the sync surface does not exist, not
			// that it is open. Same shape as authEnabled: a box nobody switched
			// on cannot be reached.
			a.fail(w, http.StatusServiceUnavailable, "booth sync is not configured")
			return
		}

		presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="booth"`)
			a.fail(w, http.StatusUnauthorized, "booth token required")
			return
		}
		sum := sha256.Sum256([]byte(presented))
		if subtle.ConstantTimeCompare(sum[:], a.boothToken) != 1 {
			a.fail(w, http.StatusUnauthorized, "booth token required")
			return
		}
		h(w, r)
	}
}
