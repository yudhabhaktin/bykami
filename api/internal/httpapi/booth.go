package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
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

// maxReport bounds a booth's report of what it is offering. A design is a name,
// a layout, a hash and a handful of cells — a few hundred bytes — so this is
// room for hundreds of them and far less than the artwork route below.
const maxReport = 256 << 10

// maxBoothArtwork is the same ceiling the catalogue applies to an upload, for
// the same reason: a larger body is not a frame.
const maxBoothArtwork = 8 << 20

// boothReport is the booth saying what it is actually offering.
//
// The one route here that is a write, and it writes nothing anyone acts on. A
// booth's frame set is the catalogue PLUS the designs compiled into its own
// binary PLUS its local -templates folder, and only the first of those is
// visible from this side — so without this the console could show four frames
// while a customer chose from eleven, which is exactly what happened. See
// internal/frames/booth.go.
//
// It is deliberately not authority for anything. Nothing read here decides what
// a booth is sent next, which frames are live, or what a season means; a booth
// that lied would mislead an operator reading a page and could not publish
// itself a frame.
func (a *API) boothReport(w http.ResponseWriter, r *http.Request) {
	var req boothReportRequest
	if !a.decodeLimited(w, r, &req, maxReport) {
		return
	}

	designs := make([]frames.Design, 0, len(req.Templates))
	for _, t := range req.Templates {
		designs = append(designs, frames.Design{
			ID: t.ID, Name: t.Name, Layout: frames.Layout(t.Layout),
			Cells: t.Cells, SHA256: t.SHA256,
		})
	}

	want, err := a.booths.Report(r.Context(), req.Outlet, designs)
	switch {
	case errors.Is(err, frames.ErrBadOutlet):
		a.fail(w, http.StatusBadRequest, "outlet must be a slug: lowercase letters, digits and dashes")
		return
	case errors.Is(err, frames.ErrBadDesign):
		// The booth's own log is where this gets read, so it carries the
		// detail: the operator who set -templates is the person who can fix it.
		a.fail(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		a.internal(w, "booth report", err)
		return
	}

	// Answering with what is missing rather than accepting the artwork inline
	// keeps the common poll — nothing changed — at one small request. Content
	// addressed, so a fleet uploads a shared built-in once between them.
	a.write(w, http.StatusOK, boothReportResponse{Want: want})
}

// boothArtworkUpload takes one design's PNG, named by its hash.
//
// PUT and not POST: the hash is the name, so sending the same bytes twice is
// the same request twice and has to mean the same thing.
func (a *API) boothArtworkUpload(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBoothArtwork))
	if err != nil {
		a.fail(w, http.StatusRequestEntityTooLarge, "artwork is larger than a frame can be")
		return
	}

	switch err := a.booths.StoreArtwork(r.Context(), r.PathValue("sha256"), body); {
	case errors.Is(err, frames.ErrArtworkMismatch):
		a.fail(w, http.StatusBadRequest, "artwork does not match the hash it was sent under")
	case err != nil:
		a.internal(w, "booth artwork upload", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type boothReportRequest struct {
	// Outlet is the agent's -outlet, the only name a booth has.
	Outlet    string        `json:"outlet"`
	Templates []boothDesign `json:"templates"`
}

type boothDesign struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	Layout string        `json:"layout"`
	Cells  []frames.Cell `json:"cells"`
	// SHA256 of the overlay artwork, or empty for a design that draws none.
	SHA256 string `json:"sha256"`
}

type boothReportResponse struct {
	// Want are the artwork hashes to upload. Empty on almost every poll, which
	// is the case this shape exists to make cheap.
	Want []string `json:"want"`
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
