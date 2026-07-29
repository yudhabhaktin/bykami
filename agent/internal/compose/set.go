package compose

import "sync/atomic"

// Set is the booth's live template list, swappable while it is being served.
//
// It exists because frames arrive from the cloud catalogue while the booth is
// running. Rebuilding the list means replacing it wholesale — a template's
// artwork, cells and name change together, and a half-applied update is a sheet
// composed with one design's cells and another's overlay.
//
// An atomic pointer to a whole slice rather than a mutex around a mutable one:
// readers are on the request path, including the shutter, and they take a
// consistent snapshot without blocking or being blocked by a sync in flight.
// The slice is never modified after Store, only replaced.
type Set struct {
	v atomic.Pointer[[]Template]
}

// NewSet returns a set holding ts.
func NewSet(ts []Template) *Set {
	s := &Set{}
	s.Store(ts)
	return s
}

// Store replaces the whole list. The caller must not modify ts afterwards.
func (s *Set) Store(ts []Template) {
	s.v.Store(&ts)
}

// All returns the current templates. The result must be treated as read-only:
// it is shared with every other reader holding the same snapshot.
func (s *Set) All() []Template {
	if p := s.v.Load(); p != nil {
		return *p
	}
	return nil
}

// ByID finds one template in the current snapshot.
func (s *Set) ByID(id string) (Template, bool) {
	for _, t := range s.All() {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}

// Len is how many designs the booth is offering.
func (s *Set) Len() int { return len(s.All()) }
