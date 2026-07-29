package httpd

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/photo"
	"github.com/bhaktiyudha/bykami/agent/internal/printer"
	"github.com/bhaktiyudha/bykami/agent/internal/session"
)

// The download gallery — the page the QR on the delivery screen points at.
//
// It exists because the booth's touchscreen is the worst keyboard in the
// building. Typing a phone number on one, to receive files the customer is
// standing next to, is a tax on the happiest moment of the visit; a QR code is
// a camera they already have out.
//
// It is served from the booth PC rather than from the cloud, which is a
// departure from design/kiosk.md's R2-backed gallery.bykami.id. The reason is
// that nothing has to leave the booth for this to work, which sidesteps the
// residency fork the cloud gallery is blocked on — and the photos are already
// here, already derived to 2048px with EXIF stripped, already deleted after
// seven days. The cloud version remains the right answer for a fleet; this is
// the right answer for one outlet that wants the QR to work now.
//
// # What guards it
//
// The unguessable URL, and nothing else. That is deliberate and it is the same
// decision the cloud design took: customers paste these links into WhatsApp
// groups, which is wanted, so any control that survives sharing is no control
// at all. What makes it defensible is that the capability is narrow and it
// expires on its own — one session's photographs, read-only, gone with the
// purge.
//
// These three routes are therefore the only ones on this server that a stranger
// is meant to reach, and they must stay that way:
//
//   - No write of any kind. /api/capture accepts 16 MB uploads; nothing here
//     accepts a body.
//   - Nothing about the session but its pictures. Not the phone number, not
//     the price, not the package, not the consent — a link forwarded to a
//     group chat must not carry the customer's number into it. The print jobs
//     are read for their sheets alone: not their state, their copies, or what
//     they cost the media roll.
//   - A file is served only after its session is checked. The token names a
//     session and the path names a photo or a print job, and without that
//     comparison one valid token would open everything the booth has ever
//     taken or composed.
type galleryPage struct {
	// Sheets are the customer's photos inside the frame they chose — the same
	// image that went to the printer. Empty until they print, which on the
	// kiosk's flow means empty only if printing failed.
	Sheets []gallerySheet
	Photos []galleryPhoto
	Token  string

	// ExpiresText is the date the photos go, already worded for a customer.
	// Formatted in Go rather than in the template because the month names are
	// Indonesian and html/template has no locale.
	ExpiresText   string
	RetentionDays int

	// StyleCSS is typed so html/template will emit it into <style> rather than
	// escaping it. It is a constant in this file, never anything from a
	// request, which is what makes that safe.
	StyleCSS template.CSS
}

type galleryPhoto struct {
	ID  string
	Num int
}

type gallerySheet struct {
	// ID is the print job's, not a filename. The handler looks the job up and
	// checks its session, which is what keeps a path the customer can edit from
	// reaching a file the customer has no claim to.
	ID  string
	Num int

	// Class reserves the sheet's shape in CSS so a tall 2×6 strip does not
	// shove the page around as it loads. Mapped from a fixed set rather than
	// interpolated from the layout string, because the CSP hashes the
	// stylesheet and an inline style attribute would be blocked by it.
	Class string
}

// gallery renders one session's photos as a page a phone can save from.
func (s *Server) gallery(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionByToken(w, r)
	if !ok {
		return
	}

	photos, err := s.Photos.BySession(r.Context(), sess.ID)
	if err != nil {
		s.fail(w, "gallery photos", err)
		return
	}

	page := galleryPage{
		Sheets: s.sheets(r.Context(), sess.ID),
		Token:  sess.ShareToken,
		// From the session rather than from the newest photo: a customer who
		// takes their last frame an hour in should not read a later date than
		// the one the booth promised them, and the purge is per photo anyway.
		ExpiresText:   indoDate(sess.OpenedAt.Add(s.retention())),
		RetentionDays: s.RetentionDays(),
		StyleCSS:      template.CSS(galleryStyle),
	}
	for _, p := range photos {
		if !p.PurgedAt.IsZero() {
			continue
		}
		page.Photos = append(page.Photos, galleryPhoto{ID: p.ID, Num: len(page.Photos) + 1})
	}

	galleryHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if len(page.Photos) == 0 {
		// Gone rather than 404: the difference between "this link was never
		// real" and "this link has done its seven days" is the difference
		// between a customer suspecting a typo and one who understands.
		w.WriteHeader(http.StatusGone)
	}
	if err := galleryTmpl.Execute(w, page); err != nil {
		// Too late for a status code — the header is already on the wire.
		s.Log.Error("httpd: render gallery", "err", err)
	}
}

// galleryPhoto serves one frame to the customer's phone.
func (s *Server) galleryPhoto(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionByToken(w, r)
	if !ok {
		return
	}

	p, err := s.Photos.Get(r.Context(), r.PathValue("photo"))
	switch {
	case errors.Is(err, photo.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		s.fail(w, "gallery photo", err)
		return
	}

	// The check this whole surface rests on. Without it the token stops being a
	// capability for one session and becomes one for the entire disk.
	//
	// Not found rather than forbidden: a 403 would confirm that the photo id
	// exists, which is a fact this caller has no business learning.
	if p.SessionID != sess.ID {
		http.NotFound(w, r)
		return
	}
	if !p.PurgedAt.IsZero() {
		http.Error(w, "this photo has been deleted", http.StatusGone)
		return
	}

	// The derivative, always — 2048px long edge, EXIF stripped, ~600 KB against
	// a 6–10 MB original. Sending originals over Indonesian mobile data is
	// hostile, and the EXIF on an original carries the camera's serial number
	// into every group chat the link reaches.
	//
	// Falling back to the original is the lesser evil against serving nothing:
	// derivation runs in a background worker, so a customer scanning the QR
	// seconds after their last shot can arrive before it has caught up.
	rel := p.DerivedPath
	if rel == "" {
		rel = p.Path
		s.Log.Info("httpd: gallery served an original; the derive worker is behind", "photo", p.ID)
	}

	galleryHeaders(w)
	if r.URL.Query().Get("dl") != "" {
		// Content-Disposition rather than the anchor's download attribute
		// alone: iOS Safari honours the header and has historically ignored
		// the attribute, and iOS is most of what will scan this code.
		w.Header().Set("Content-Disposition", `attachment; filename="bykami-`+p.ID[:8]+`.jpg"`)
	}
	http.ServeFile(w, r, filepath.Join(s.Root, filepath.FromSlash(rel)))
}

// galleryPrint serves one composed sheet — the customer's frames inside the
// template they picked, with their filter, exactly as it went to the printer.
//
// The sheet rather than a fresh composition, because the sheet is the thing
// they are holding: recomposing here would need the template, the filter and
// the chosen frames stored somewhere, and would still quietly differ from the
// print the moment any of the three changed. It is also already the right file
// to hand over — composed from the originals at 300 dpi, and re-encoded by
// compose, which writes no EXIF and so carries no camera serial into whatever
// group chat this reaches.
func (s *Server) galleryPrint(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionByToken(w, r)
	if !ok {
		return
	}

	job, err := s.Printer.Get(r.Context(), r.PathValue("sheet"))
	switch {
	case errors.Is(err, printer.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		s.fail(w, "gallery sheet", err)
		return
	}

	// The same check the photo route rests on, for the same reason, and not
	// found for the same reason: a 403 would confirm the job exists.
	if job.SessionID != sess.ID {
		http.NotFound(w, r)
		return
	}

	path := filepath.Join(s.Root, filepath.FromSlash(job.SheetPath))
	if _, err := os.Stat(path); err != nil {
		// Sheets are swept by file age alongside the originals, so a job whose
		// file has gone is retention working rather than anything broken. Same
		// answer as a purged photo, because to the customer it is one event.
		http.Error(w, "this photo has been deleted", http.StatusGone)
		return
	}

	galleryHeaders(w)
	if r.URL.Query().Get("dl") != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="bykami-frame-`+job.ID[:8]+`.jpg"`)
	}
	http.ServeFile(w, r, path)
}

// sheets lists a session's composed prints, newest first.
//
// Deduplicated by content, because a reprint composes the same picture again
// under a new filename. Most packages include more than one print and most
// customers use them, so without this the ordinary case is a page showing the
// identical strip two or three times — which reads as a bug rather than as
// generosity. Prints that genuinely differ, a second template or another
// filter, hash differently and all survive.
func (s *Server) sheets(ctx context.Context, sessionID string) []gallerySheet {
	jobs, err := s.Printer.BySession(ctx, sessionID)
	if err != nil {
		// Logged and dropped rather than failing the request. The frames are
		// what the customer scanned the code for; losing the framed version is
		// a worse page, losing the page is a worse visit.
		s.Log.Error("httpd: gallery sheets", "session", sessionID, "err", err)
		return nil
	}

	var out []gallerySheet
	seen := make(map[[sha256.Size]byte]bool, len(jobs))
	for _, j := range jobs {
		sum, err := hashFile(filepath.Join(s.Root, filepath.FromSlash(j.SheetPath)))
		if err != nil {
			// Swept, or never written because the job was refused. Either way
			// listing it would give the customer a broken image.
			continue
		}
		if seen[sum] {
			continue
		}
		seen[sum] = true
		out = append(out, gallerySheet{ID: j.ID, Num: len(out) + 1, Class: sheetClass(j.Layout)})
	}
	return out
}

// sheetClass names the CSS rule holding this layout's shape. An unknown layout
// gets none, which costs a little layout shift and renders correctly.
func sheetClass(l printer.Layout) string {
	switch l {
	case printer.Layout4R:
		return "sheet-4r"
	case printer.LayoutStrip:
		return "sheet-strip"
	case printer.Layout6x8:
		return "sheet-6x8"
	}
	return ""
}

func hashFile(path string) ([sha256.Size]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [sha256.Size]byte{}, err
	}
	return [sha256.Size]byte(h.Sum(nil)), nil
}

// sessionByToken resolves the {token} path value, answering the request itself
// when it does not name a session.
func (s *Server) sessionByToken(w http.ResponseWriter, r *http.Request) (session.Session, bool) {
	sess, err := s.Sessions.ByShareToken(r.Context(), r.PathValue("token"))
	switch {
	case errors.Is(err, session.ErrNotFound):
		http.NotFound(w, r)
		return session.Session{}, false
	case err != nil:
		s.fail(w, "gallery session", err)
		return session.Session{}, false
	}
	return sess, true
}

// retention is how long the booth keeps photographs, and therefore how long a
// download link works. Defaulted here rather than required of every caller so
// that a zero value cannot silently promise a customer nothing.
func (s *Server) retention() time.Duration {
	if s.Retention <= 0 {
		return defaultRetention
	}
	return s.Retention
}

// RetentionDays is the window in whole days, which is the only unit a customer
// is ever shown it in.
func (s *Server) RetentionDays() int {
	return int(s.retention() / (24 * time.Hour))
}

// isGalleryPath reports whether a request is for the download gallery.
//
// Used by the guard to let these two routes past the booth's access token. A
// customer cannot hold that token — it drives the booth — so requiring it here
// would mean the QR only worked for the operator.
//
// It matches the registered patterns exactly, and it has to. A prefix test on
// "/g/" looked equivalent and was not: the mux falls through to the kiosk UI on
// anything it cannot route, so "/g/", "/g/x/y/z" and any other near-miss were
// served the booth's own interface with no token at all. A test asserts that
// they are refused.
//
// Every gallery route added must be added here too — which is the cost of the
// exemption, and the reason there are only three.
func isGalleryPath(p string) bool {
	rest, ok := strings.CutPrefix(p, "/g/")
	if !ok {
		return false
	}
	token, sub, nested := strings.Cut(rest, "/")
	if token == "" {
		return false
	}
	if !nested {
		return true // GET /g/{token}
	}
	// GET /g/{token}/p/{photo} and /g/{token}/s/{sheet}, and nothing below
	// either of them.
	kind, id, ok := strings.Cut(sub, "/")
	return ok && (kind == "p" || kind == "s") && id != "" && !strings.Contains(id, "/")
}

// galleryHeaders locks the page down to what it actually is: some text and
// some same-origin JPEGs.
//
// The CSP is the load-bearing one. `default-src 'none'` with an explicit
// `img-src 'self'` means that if somebody adds a lightbox to this page in eight
// months, it does not run — which is the failure this page is most likely to
// suffer, because a photo gallery is exactly the thing people want to add
// JavaScript to. There is a test asserting the header is present.
func galleryHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; img-src 'self'; style-src '"+galleryStyleHash+"'; "+
			"base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	// A customer's face is not a search result.
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noimageindex, noarchive")
	// The URL is the secret, so it must not travel in a Referer to anywhere.
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
}

// indoDate is "5 Agustus 2026". Written out rather than 05/08/2026 because a
// numeric date is ambiguous to half the internet and this one is a deadline.
func indoDate(t time.Time) string {
	months := [...]string{"Januari", "Februari", "Maret", "April", "Mei", "Juni",
		"Juli", "Agustus", "September", "Oktober", "November", "Desember"}
	return strconv.Itoa(t.Day()) + " " + months[t.Month()-1] + " " + strconv.Itoa(t.Year())
}

// defaultRetention matches purge.DefaultAge. Restated rather than imported
// because httpd importing the purge worker to render a sentence would be the
// wrong dependency; the wiring in cmd passes the real value and there is a test
// that these two agree.
const defaultRetention = 7 * 24 * time.Hour

// galleryStyle is hashed into the CSP below, so it may not move into a `style`
// attribute or a second block without the hash following it.
const galleryStyle = `
:root { color-scheme: light }
* { box-sizing: border-box }
body {
  margin: 0; padding: 24px 16px 64px;
  background: #f6efe3; color: #1a1a1a;
  font-family: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
  line-height: 1.5;
}
main { max-width: 46rem; margin: 0 auto }
h1 { font-size: 1.75rem; letter-spacing: -0.5px; margin: 0 0 4px }
h2 { font-size: 1.05rem; letter-spacing: -0.2px; margin: 30px 0 2px }
.brand { font-weight: 700; font-size: .8rem; letter-spacing: .14em; text-transform: uppercase; color: #6b6257 }
p { margin: 0 0 4px; color: #6b6257 }
.sheets { display: flex; flex-wrap: wrap; gap: 12px; margin-top: 12px }
.sheets a {
  display: block; border: 2px solid #1a1a1a; border-radius: 14px;
  overflow: hidden; background: #fffcf7; box-shadow: 3px 3px 0 #1a1a1a;
  height: min(58vh, 22rem);
}
/* Sized from the height so a 2x6 strip and a 4R sheet sit side by side at the
   same scale instead of one of them filling the phone. */
.sheets img { display: block; height: 100%; width: auto; max-width: 100% }
.sheet-4r { aspect-ratio: 2/3 }
.sheet-strip { aspect-ratio: 1/3 }
.sheet-6x8 { aspect-ratio: 3/4 }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(9rem, 1fr)); gap: 12px; margin-top: 12px }
.grid a {
  display: block; border: 2px solid #1a1a1a; border-radius: 14px;
  overflow: hidden; background: #fffcf7; box-shadow: 3px 3px 0 #1a1a1a;
}
.grid img { display: block; width: 100%; height: 100%; aspect-ratio: 4/3; object-fit: cover }
.note { margin-top: 32px; padding: 12px 16px; border: 2px solid #1a1a1a; border-radius: 12px; background: #dcebe1; color: #1a1a1a }
.gone { margin-top: 24px; padding: 16px; border: 2px solid #1a1a1a; border-radius: 12px; background: #e7b23f }
`

// galleryStyleHash is the CSP source expression for the block above, computed
// once at startup so the two can never drift apart. Hardcoding a hash is how
// this page ends up quietly unstyled after somebody fixes a margin.
var galleryStyleHash = "sha256-" +
	base64.StdEncoding.EncodeToString(func() []byte {
		sum := sha256.Sum256([]byte(galleryStyle))
		return sum[:]
	}())

// No script, and no way to add one without changing the CSP above. Indonesian,
// because the person reading it just walked out of the booth.
var galleryTmpl = template.Must(template.New("gallery").Parse(`<!doctype html>
<html lang="id">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<title>Foto kamu · bykami</title>
<style>{{.StyleCSS}}</style>
</head>
<body>
<main>
  <span class="brand">bykami · self photo studio</span>
  <h1>Ini foto kamu 🎉</h1>
{{if .Photos}}
  <p>Ketuk gambar untuk menyimpannya ke HP.</p>
{{if .Sheets}}
  <h2>Versi bingkai</h2>
  <p>Sama persis dengan hasil cetakmu.</p>
  <div class="sheets">
{{range .Sheets}}
    <a href="/g/{{$.Token}}/s/{{.ID}}?dl=1"><img class="{{.Class}}" src="/g/{{$.Token}}/s/{{.ID}}" alt="Cetakan {{.Num}}"></a>
{{end}}
  </div>
{{end}}
  <h2>Foto asli</h2>
  <p>Tanpa bingkai, satu per satu — {{len .Photos}} foto.</p>
  <div class="grid">
{{range .Photos}}
    <a href="/g/{{$.Token}}/p/{{.ID}}?dl=1"><img src="/g/{{$.Token}}/p/{{.ID}}" alt="Foto {{.Num}}" loading="lazy"></a>
{{end}}
  </div>
  <div class="note">
    <strong>Simpan sekarang, ya.</strong> Foto ini otomatis terhapus pada
    {{.ExpiresText}} — setelah itu link ini kosong. Siapa pun yang punya link
    bisa membukanya, jadi bagikan seperlunya.
  </div>
{{else}}
  <div class="gone">
    <strong>Foto ini sudah terhapus.</strong> Kami menyimpan foto selama
    {{.RetentionDays}} hari, lalu menghapusnya otomatis.
  </div>
{{end}}
</main>
</body>
</html>
`))
