package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"testing"

	"github.com/bhaktiyudha/bykami/agent/internal/compose"
)

// The designs compiled into this binary are the ones the operator console
// cannot see any other way: they have no catalogue row, their artwork exists
// only inside the executable, and a booth offers them alongside whatever it
// syncs. If they stop being reportable the console goes quietly back to showing
// four frames while a customer chooses from eleven, which is the failure this
// whole path exists to end.
func TestEveryBuiltInDesignIsReportable(t *testing.T) {
	builtin, err := compose.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}
	if len(builtin) == 0 {
		t.Fatal("no built-in templates, so there is nothing to report")
	}

	got := reportable(builtin, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if len(got) != len(builtin) {
		t.Fatalf("reported %d designs for %d templates", len(got), len(builtin))
	}

	for i, d := range got {
		src := builtin[i]
		switch {
		case d.ID != src.ID || d.Name != src.Name:
			t.Errorf("design %d = %q/%q, want %q/%q", i, d.ID, d.Name, src.ID, src.Name)
		case d.Layout != string(src.Layout):
			t.Errorf("%s: layout = %q, want %q", d.ID, d.Layout, src.Layout)
		case len(d.Cells) != len(src.Cells):
			// The console draws the slots from these, over the artwork below.
			t.Errorf("%s: %d cells reported, want %d", d.ID, len(d.Cells), len(src.Cells))
		case d.SHA256 == "" || d.PNG == nil:
			t.Errorf("%s: reported with no artwork, so the console can only show a name", d.ID)
		}
	}
}

// The hash is the whole upload protocol: it is what the server compares against
// what it already holds, and what it verifies the bytes against on arrival.
func TestAReportedHashIsTheHashOfTheReportedBytes(t *testing.T) {
	builtin, err := compose.Builtin()
	if err != nil {
		t.Fatalf("Builtin: %v", err)
	}

	for _, d := range reportable(builtin, slog.New(slog.NewTextHandler(io.Discard, nil))) {
		sum := sha256.Sum256(d.PNG)
		if got := hex.EncodeToString(sum[:]); got != d.SHA256 {
			t.Errorf("%s: hash = %s, but the bytes hash to %s", d.ID, d.SHA256, got)
		}
	}
}
