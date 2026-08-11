package admin

import (
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/frames"
	"github.com/bhaktiyudha/bykami/api/internal/identity"
)

// maxUpload bounds a frame PNG. The house strips are around 25 KB; a
// photographic background at 1800×2400 is a few hundred. Eight megabytes is
// far above anything real and far below what would trouble a 1 GB box, and the
// limit exists so that a wrong file — a video, a RAW — is refused at the socket
// rather than after it has been read into memory.
const maxUpload = 8 << 20

// wib is Western Indonesian Time as a fixed offset rather than a named zone.
//
// Indonesia has no daylight saving, so +07:00 is exact all year, and a fixed
// zone needs no tzdata on the host — a season that silently shifted by seven
// hours because a container image shipped without the zone database would put a
// Lebaran frame on the booth the evening before, or take it away the evening
// after.
var wib = time.FixedZone("WIB", 7*60*60)

// boothView is one booth's reported set, flattened for the template. Formatted
// here rather than there for the reason calendarRow exists: a template cannot
// decide what counts as out of date, and a policy inside one is a policy nobody
// can test.
type boothView struct {
	Outlet   string
	Reported string
	// Silent means the booth has not reported in long enough that the list
	// below is a historical record rather than a current one.
	Silent  bool
	Designs []designView
}

// designView is one frame a booth is offering.
type designView struct {
	ID     string
	Name   string
	Layout string
	Cells  []frames.Cell

	// The sheet this layout prints, which is the space Cells are measured in.
	// Not the artwork's own pixel size: the overlay is scaled onto the sheet
	// when it is composed, so the sheet is what the slots sit on.
	Width  int
	Height int

	// Art is the artwork URL, empty for a design that draws no overlay — a
	// plain photo template has no frame to show.
	Art string

	// FromCatalogue distinguishes a design an operator can do something about
	// from one that lives inside the agent binary, where the only way to remove
	// it is to ship a new one.
	FromCatalogue bool
}

// boothSilent is how long a booth may go unheard-from before the page says so.
//
// Three polls. The agent reports every five minutes, so one missed report is a
// blip on a shop's internet connection and three in a row is a booth that is
// off, unplugged, or no longer talking to this server — which is the difference
// between a list that is a few minutes old and a list that is a week old.
const boothSilent = 15 * time.Minute

func (c *Console) frameIndex(w http.ResponseWriter, r *http.Request, op identity.User) {
	p := page{
		Title:    "Frame",
		Operator: op.Phone,
		CSRF:     csrfToken(r),
		Sheets:   frames.SheetSizes(),
	}
	if r.URL.Query().Get("ok") != "" {
		p.Notice = r.URL.Query().Get("ok")
	}
	if msg := r.URL.Query().Get("err"); msg != "" {
		p.Error = msg
	}

	list, err := c.frameCat.List(r.Context())
	if err != nil {
		c.log.Error("admin: list frames", "err", err)
		p.Error = "Gagal memuat katalog frame."
		c.render(w, r, http.StatusInternalServerError, "frames.html", p)
		return
	}
	p.Frames = list

	// The booths are a separate failure. A catalogue that loaded is worth
	// showing even if the reports did not, because the upload form is on this
	// page and it still works.
	reports, err := c.booths.All(r.Context())
	if err != nil {
		c.log.Error("admin: list booth reports", "err", err)
		p.Error = "Gagal memuat daftar frame di booth."
		c.render(w, r, http.StatusOK, "frames.html", p)
		return
	}
	p.Booths, p.OnBooth = boothViews(reports, list)
	p.BoothsKnown = len(reports) > 0
	c.render(w, r, http.StatusOK, "frames.html", p)
}

// boothViews flattens the reports into what the page draws, and returns the set
// of catalogue ids that reached at least one booth.
//
// The second return is what turns "Tayang di booth" from a claim into a fact.
// It used to mean only "published", so a frame that had been published thirty
// seconds ago — or one a booth had been failing to download for a week — read
// exactly like one a customer was already choosing.
func boothViews(reports []frames.Booth, catalogue []frames.Frame) ([]boothView, map[string]bool) {
	inCatalogue := make(map[string]bool, len(catalogue))
	for _, f := range catalogue {
		inCatalogue[f.ID] = true
	}

	now := time.Now().UTC()
	onBooth := map[string]bool{}
	out := make([]boothView, 0, len(reports))
	for _, b := range reports {
		v := boothView{
			Outlet:   b.Outlet,
			Reported: b.ReportedAt.In(wib).Format("2 Jan 2006, 15:04"),
			Silent:   now.Sub(b.ReportedAt) > boothSilent,
		}
		for _, d := range b.Designs {
			w, h := d.Layout.Sheet()
			dv := designView{
				Name: d.Name, ID: d.ID, Layout: string(d.Layout),
				Cells: d.Cells, Width: w, Height: h,
				FromCatalogue: inCatalogue[d.ID],
			}
			if d.SHA256 != "" {
				dv.Art = "/booth/art/" + d.SHA256
			}
			if dv.FromCatalogue {
				onBooth[d.ID] = true
			}
			v.Designs = append(v.Designs, dv)
		}
		out = append(out, v)
	}
	return out, onBooth
}

// boothArt serves a design's artwork by its hash.
//
// Its own route rather than /frames/{id}/art.png, because most of what a booth
// offers has no catalogue row to hang an id off — the built-in designs exist
// only inside the agent binary. The hash is the name for the same reason it is
// the sync protocol: identical artwork on five booths is one stored copy.
func (c *Console) boothArt(w http.ResponseWriter, r *http.Request, _ identity.User) {
	sum := strings.TrimSuffix(r.PathValue("sha256"), ".png")

	art, err := c.booths.Artwork(r.Context(), sum)
	if errors.Is(err, frames.ErrNoArtwork) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		c.log.Error("admin: booth art", "err", err)
		http.Error(w, "gagal memuat gambar", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("X-Content-Type-Options", "nosniff")
	// Genuinely immutable: the URL is the hash of the bytes it returns, so a
	// design that changes is a different address.
	h.Set("ETag", `"`+sum+`"`)
	h.Set("Cache-Control", "private, max-age=300")
	if r.Header.Get("If-None-Match") == `"`+sum+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if _, err := w.Write(art); err != nil {
		c.log.Error("admin: write booth art", "err", err)
	}
}

// frameUpload takes the PNG and reads the rest of the design out of it.
func (c *Console) frameUpload(w http.ResponseWriter, r *http.Request, op identity.User) {
	if !validCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	if err := r.ParseMultipartForm(maxUpload); err != nil {
		c.backToFrames(w, r, fmt.Sprintf("File terlalu besar. Maksimal %d MB.", maxUpload>>20))
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("art")
	if err != nil {
		c.backToFrames(w, r, "Pilih file PNG frame-nya.")
		return
	}
	defer file.Close()

	art := make([]byte, header.Size)
	if _, err := io.ReadFull(file, art); err != nil {
		c.backToFrames(w, r, "Gagal membaca file.")
		return
	}

	from, err := parseDay(r.FormValue("active_from"))
	if err != nil {
		c.backToFrames(w, r, "Tanggal mulai tidak valid.")
		return
	}
	// The form asks for the last day the frame runs, which is how a person
	// thinks about a season. The catalogue stores the instant it stops, so an
	// inclusive date becomes the midnight after it — otherwise a frame set to
	// end on Lebaran would vanish at the start of Lebaran.
	until, err := parseDay(r.FormValue("active_until"))
	if err != nil {
		c.backToFrames(w, r, "Tanggal selesai tidak valid.")
		return
	}
	if !until.IsZero() {
		until = until.AddDate(0, 0, 1)
	}

	f, err := c.frameCat.Create(r.Context(), frames.NewFrame{
		Name:        r.FormValue("name"),
		Group:       r.FormValue("group"),
		PNG:         art,
		ActiveFrom:  from,
		ActiveUntil: until,
	})
	if err != nil {
		c.backToFrames(w, r, uploadMessage(err))
		return
	}

	c.log.Info("admin: frame uploaded", "operator", op.Phone, "frame", f.ID,
		"layout", f.Layout, "cells", len(f.Cells), "bytes", f.Bytes)
	c.redirect(w, r, "/frames?ok="+urlQueryEscape(fmt.Sprintf(
		"%q tersimpan: %d slot foto terdeteksi. Periksa dulu, lalu terbitkan.", f.Name, len(f.Cells))))
}

// uploadMessage turns a catalogue error into something an operator can act on.
// Every one of these is a mistake in the file, so the message says what to
// change about the file.
func uploadMessage(err error) string {
	switch {
	case errors.Is(err, frames.ErrNoName):
		return "Beri nama frame-nya."
	case errors.Is(err, frames.ErrDuplicate):
		return "Sudah ada frame dengan nama itu. Pakai nama lain."
	case errors.Is(err, frames.ErrNotPNG):
		return "File harus PNG. JPEG tidak punya area transparan, jadi tidak ada tempat foto muncul."
	case errors.Is(err, frames.ErrOpaque):
		return "PNG ini tidak punya area transparan. Lubang tempat foto harus benar-benar transparan, bukan putih."
	case errors.Is(err, frames.ErrNoCells):
		return "Tidak ada lubang foto yang terbaca. Lubangnya boleh berbentuk apa saja — kotak, bulat, hati — tapi harus cukup besar, minimal 1% dari kanvas."
	case errors.Is(err, frames.ErrSheetSize):
		return "Ukuran kanvas tidak cocok. Yang bisa dicetak: " + frames.SheetSizes() + "."
	case errors.Is(err, frames.ErrBadWindow):
		return "Tanggal selesai harus setelah tanggal mulai."
	default:
		return "Gagal menyimpan frame."
	}
}

// frameArt serves the stored PNG to the console's own preview.
//
// Staff-only like every other frame route. The artwork is not secret, but this
// is the operator console and an unauthenticated image route here would be a
// way to enumerate the catalogue of a business that has not launched it.
func (c *Console) frameArt(w http.ResponseWriter, r *http.Request, _ identity.User) {
	art, sum, err := c.frameCat.Artwork(r.Context(), r.PathValue("id"))
	if errors.Is(err, frames.ErrNoFrame) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		c.log.Error("admin: frame art", "err", err)
		http.Error(w, "gagal memuat gambar", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("X-Content-Type-Options", "nosniff")
	// The hash is the content, so this is genuinely immutable: replacing the
	// artwork means a new frame with a new id.
	h.Set("ETag", `"`+sum+`"`)
	h.Set("Cache-Control", "private, max-age=300")
	if match := r.Header.Get("If-None-Match"); match == `"`+sum+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if _, err := w.Write(art); err != nil {
		c.log.Error("admin: write frame art", "err", err)
	}
}

func (c *Console) framePublish(w http.ResponseWriter, r *http.Request, op identity.User) {
	if !validCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	publish := r.FormValue("publish") == "1"

	if err := c.frameCat.SetPublished(r.Context(), id, publish); err != nil {
		c.log.Error("admin: publish frame", "err", err, "frame", id)
		c.backToFrames(w, r, "Gagal mengubah status frame.")
		return
	}

	c.log.Info("admin: frame published", "operator", op.Phone, "frame", id, "published", publish)
	if publish {
		c.redirect(w, r, "/frames?ok="+urlQueryEscape("Frame diterbitkan. Booth akan menariknya beberapa menit lagi."))
		return
	}
	c.redirect(w, r, "/frames?ok="+urlQueryEscape("Frame ditarik dari booth."))
}

func (c *Console) frameSeason(w http.ResponseWriter, r *http.Request, op identity.User) {
	if !validCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")

	from, err := parseDay(r.FormValue("active_from"))
	if err != nil {
		c.backToFrames(w, r, "Tanggal mulai tidak valid.")
		return
	}
	until, err := parseDay(r.FormValue("active_until"))
	if err != nil {
		c.backToFrames(w, r, "Tanggal selesai tidak valid.")
		return
	}
	if !until.IsZero() {
		until = until.AddDate(0, 0, 1)
	}

	switch err := c.frameCat.SetSeason(r.Context(), id, from, until); {
	case errors.Is(err, frames.ErrBadWindow):
		c.backToFrames(w, r, "Tanggal selesai harus setelah tanggal mulai.")
	case err != nil:
		c.log.Error("admin: frame season", "err", err, "frame", id)
		c.backToFrames(w, r, "Gagal menyimpan musim.")
	default:
		c.log.Info("admin: frame season set", "operator", op.Phone, "frame", id,
			"from", r.FormValue("active_from"), "until", r.FormValue("active_until"))
		c.redirect(w, r, "/frames?ok="+urlQueryEscape("Musim tersimpan."))
	}
}

func (c *Console) frameDelete(w http.ResponseWriter, r *http.Request, op identity.User) {
	if !validCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	if err := c.frameCat.Delete(r.Context(), id); err != nil {
		c.log.Error("admin: delete frame", "err", err, "frame", id)
		c.backToFrames(w, r, "Gagal menghapus frame.")
		return
	}
	c.log.Info("admin: frame deleted", "operator", op.Phone, "frame", id)
	c.redirect(w, r, "/frames?ok="+urlQueryEscape("Frame dihapus."))
}

func (c *Console) backToFrames(w http.ResponseWriter, r *http.Request, msg string) {
	c.redirect(w, r, "/frames?err="+urlQueryEscape(msg))
}

// parseDay reads a date input as midnight in Jakarta. Empty means unbounded,
// which is the common case: most frames have no season at all.
func parseDay(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	return time.ParseInLocation("2006-01-02", s, wib)
}

// dayValue renders an instant back into the form's inclusive date. It is the
// inverse of the +1 day the handlers apply to an end date, so a season that is
// saved and reopened shows the dates that were typed.
func dayValue(t time.Time, inclusiveEnd bool) string {
	if t.IsZero() {
		return ""
	}
	t = t.In(wib)
	if inclusiveEnd {
		t = t.AddDate(0, 0, -1)
	}
	return t.Format("2006-01-02")
}

// seasonText describes a window in words for the frame's card.
func seasonText(f frames.Frame) string {
	const d = "2 Jan 2006"
	from, until := f.ActiveFrom.In(wib), f.ActiveUntil.In(wib).AddDate(0, 0, -1)
	switch {
	case f.ActiveFrom.IsZero() && f.ActiveUntil.IsZero():
		return "sepanjang tahun"
	case f.ActiveFrom.IsZero():
		return "sampai " + until.Format(d)
	case f.ActiveUntil.IsZero():
		return "mulai " + from.Format(d)
	}
	return from.Format(d) + " – " + until.Format(d)
}

// slotStyle positions one detected cell over the preview, as percentages of the
// frame's own geometry.
//
// Percentages rather than pixels because the preview is a few hundred pixels
// tall and the sheet is 1800. Drawn from the same numbers the booth composes
// with, so what an operator checks here is what the printer will do — which is
// the entire purpose of showing it.
func slotStyle(c frames.Cell, w, h int) template.CSS {
	return template.CSS(fmt.Sprintf("left:%.4f%%;top:%.4f%%;width:%.4f%%;height:%.4f%%",
		100*float64(c.X)/float64(w), 100*float64(c.Y)/float64(h),
		100*float64(c.W)/float64(w), 100*float64(c.H)/float64(h)))
}
