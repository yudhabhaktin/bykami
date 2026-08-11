package framesync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// reportServer is the cloud side of the report: it remembers what a booth said
// and asks for artwork it has not been given.
type reportServer struct {
	outlet    string
	templates []Design
	stored    map[string][]byte
	reports   int
	reject    int
}

func (s *reportServer) handler(t *testing.T) *httptest.Server {
	t.Helper()
	s.stored = map[string][]byte{}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/booth/templates", func(w http.ResponseWriter, r *http.Request) {
		s.reports++
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if s.reject != 0 {
			http.Error(w, "no", s.reject)
			return
		}
		var body struct {
			Outlet    string   `json:"outlet"`
			Templates []Design `json:"templates"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.outlet, s.templates = body.Outlet, body.Templates

		var want []string
		for _, d := range body.Templates {
			if d.SHA256 == "" {
				continue
			}
			if _, ok := s.stored[d.SHA256]; !ok {
				want = append(want, d.SHA256)
			}
		}
		_ = json.NewEncoder(w).Encode(struct {
			Want []string `json:"want"`
		}{want})
	})

	mux.HandleFunc("PUT /v1/booth/templates/art/{sha256}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != r.PathValue("sha256") {
			http.Error(w, "hash mismatch", http.StatusBadRequest)
			return
		}
		s.stored[r.PathValue("sha256")] = body
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func design(t *testing.T, id, name string, art []byte) Design {
	t.Helper()
	sum := sha256.Sum256(art)
	return Design{
		ID: id, Name: name, Layout: "4r",
		Cells:  []Cell{{X: 624, Y: 147, W: 560, H: 385}},
		SHA256: hex.EncodeToString(sum[:]), PNG: art,
	}
}

// The report exists so the console can see designs that are not in the
// catalogue at all — the ones compiled into this binary.
func TestReportSendsTheWholeSetAndUploadsArtworkOnce(t *testing.T) {
	art := strip(t, 1)
	s := &reportServer{}
	srv := s.handler(t)

	w := New(srv.URL, token, "jajag", t.TempDir(), time.Minute,
		func() error { return nil },
		func() []Design { return []Design{design(t, "gacoan-1-taplak", "Taplak Gacoan", art)} },
		discard())

	if err := w.Report(context.Background()); err != nil {
		t.Fatalf("Report: %v", err)
	}
	switch {
	case s.outlet != "jajag":
		t.Errorf("outlet = %q, want jajag", s.outlet)
	case len(s.templates) != 1 || s.templates[0].ID != "gacoan-1-taplak":
		t.Fatalf("templates = %+v, want the one built-in design", s.templates)
	case len(s.templates[0].Cells) != 1:
		t.Error("the cells did not travel, so the console cannot draw the slots")
	}

	sum := sha256.Sum256(art)
	if got, ok := s.stored[hex.EncodeToString(sum[:])]; !ok || string(got) != string(art) {
		t.Fatal("the artwork was not uploaded, so the console has nothing to show")
	}

	// The second report costs one small request. This is what keeps a
	// five-minute loop off a shop's internet connection.
	if err := w.Report(context.Background()); err != nil {
		t.Fatalf("second Report: %v", err)
	}
	if len(s.stored) != 1 {
		t.Errorf("stored %d artworks, want 1 — the same bytes were uploaded twice", len(s.stored))
	}
}

// The report is not on the selling path, and neither is its failure.
func TestAFailedReportIsNotFatal(t *testing.T) {
	s := &reportServer{reject: http.StatusInternalServerError}
	srv := s.handler(t)

	w := New(srv.URL, token, "jajag", t.TempDir(), time.Minute, func() error { return nil },
		func() []Design { return []Design{design(t, "one", "One", strip(t, 1))} }, discard())

	if err := w.Report(context.Background()); err == nil {
		t.Error("a rejected report returned no error, so nothing would be logged")
	}

	// Run must survive it. A booth whose console cannot be updated still sells.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err == nil {
		t.Error("Run returned without the context being done")
	}
}

// A booth with nothing to say still says so, or a booth offering no designs
// would look identical to one that has never reported.
func TestReportingAnEmptySetSendsAnEmptyList(t *testing.T) {
	s := &reportServer{}
	srv := s.handler(t)

	w := New(srv.URL, token, "jajag", t.TempDir(), time.Minute, func() error { return nil },
		func() []Design { return nil }, discard())

	if err := w.Report(context.Background()); err != nil {
		t.Fatalf("Report: %v", err)
	}
	if s.reports != 1 || s.outlet != "jajag" || len(s.templates) != 0 {
		t.Errorf("reports=%d outlet=%q templates=%+v, want one report of an empty set",
			s.reports, s.outlet, s.templates)
	}
}

// A booth that was never given a snapshot is one whose caller has nothing to
// report. It must not send an empty set and wipe the console's list.
func TestReportIsSilentWithoutASnapshot(t *testing.T) {
	s := &reportServer{}
	srv := s.handler(t)

	w := New(srv.URL, token, "jajag", t.TempDir(), time.Minute, func() error { return nil }, nil, discard())
	if err := w.Report(context.Background()); err != nil {
		t.Fatalf("Report: %v", err)
	}
	if s.reports != 0 {
		t.Errorf("reports = %d, want 0", s.reports)
	}
}
