package main

import (
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/api/internal/frames"
)

func TestParseDayTreatsDashAsUnbounded(t *testing.T) {
	for _, in := range []string{"", "-", "   "} {
		got, err := parseDay(in)
		if err != nil {
			t.Errorf("parseDay(%q): %v", in, err)
		}
		if !got.IsZero() {
			t.Errorf("parseDay(%q) = %v, want the zero time", in, got)
		}
	}

	got, err := parseDay("2027-02-08")
	if err != nil {
		t.Fatalf("parseDay: %v", err)
	}
	if got.Year() != 2027 || got.Month() != time.February || got.Day() != 8 {
		t.Errorf("parseDay = %v", got)
	}

	if _, err := parseDay("8 Feb 2027"); err == nil {
		t.Error("a date in the wrong format was accepted")
	}
}

// The end of a season is stored as the instant it stops and shown as the last
// day it runs. Off by one here means a Lebaran frame that vanishes on Lebaran.
func TestSeasonShowsTheLastDayItRuns(t *testing.T) {
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	for _, tc := range []struct {
		name  string
		frame frames.Frame
		want  string
	}{
		{"no bounds", frames.Frame{}, "always"},
		{
			"both ends",
			frames.Frame{ActiveFrom: day("2027-02-08"), ActiveUntil: day("2027-03-10")},
			"2027-02-08→2027-03-09",
		},
		{"open start", frames.Frame{ActiveUntil: day("2027-03-10")}, "until 2027-03-09"},
		{"open end", frames.Frame{ActiveFrom: day("2027-02-08")}, "from 2027-02-08"},
	} {
		if got := season(tc.frame); got != tc.want {
			t.Errorf("%s: season = %q, want %q", tc.name, got, tc.want)
		}
	}
}
