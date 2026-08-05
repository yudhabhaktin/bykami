package shutter_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bhaktiyudha/bykami/agent/internal/shutter"
)

func TestFireCallsTheURLAndAcceptsSuccess(t *testing.T) {
	var called int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if got := r.URL.Query().Get("slc"); got != "capturenoaf" {
			t.Errorf("query lost on the way out: slc = %q", got)
		}
		// What digiCamControl actually answers.
		w.Write([]byte("OK\n"))
	}))
	t.Cleanup(srv.Close)

	r, err := shutter.New(srv.URL+"/?slc=capturenoaf", time.Second)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := r.Fire(t.Context()); err != nil {
		t.Fatalf("fire: %v", err)
	}
	if called != 1 {
		t.Fatalf("the camera was asked to fire %d times", called)
	}
}

// A tool that says nothing and returns 200 has agreed. Not every shutter is
// digiCamControl, and an empty body is the ordinary shape of "fine".
func TestEmptyBodyIsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)

	r, _ := shutter.New(srv.URL, time.Second)
	if err := r.Fire(t.Context()); err != nil {
		t.Fatalf("an empty 200 was treated as a failure: %v", err)
	}
}

// The failure this type exists for. digiCamControl reports a missing camera as
// a 200 with the problem in the text, so a booth reading only the status code
// counts down, photographs nobody, and tells the customer it went fine.
func TestErrorInsideA200IsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("No camera is connected"))
	}))
	t.Cleanup(srv.Close)

	r, _ := shutter.New(srv.URL, time.Second)
	err := r.Fire(t.Context())
	if err == nil {
		t.Fatal("a camera that refused to fire was reported as a photograph taken")
	}
	if !strings.Contains(err.Error(), "No camera is connected") {
		t.Fatalf("the reason did not survive into the error: %v", err)
	}
}

func TestHTTPFailureIsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	r, _ := shutter.New(srv.URL, time.Second)
	if err := r.Fire(t.Context()); err == nil {
		t.Fatal("a 500 was treated as a photograph taken")
	}
}

// Nothing listening is the everyday failure: the tool that owns the camera was
// closed, or never started with the booth.
func TestUnreachableIsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	r, _ := shutter.New(url, 500*time.Millisecond)
	if err := r.Fire(t.Context()); err == nil {
		t.Fatal("a closed port was treated as a photograph taken")
	}
}

// A whole error page must not become a whole log line.
func TestLongRepliesAreCutToTheFirstLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Camera busy\n" + strings.Repeat("stack trace\n", 500)))
	}))
	t.Cleanup(srv.Close)

	r, _ := shutter.New(srv.URL, time.Second)
	err := r.Fire(t.Context())
	if err == nil {
		t.Fatal("want a failure")
	}
	if len(err.Error()) > 200 {
		t.Fatalf("the error is %d bytes of log noise", len(err.Error()))
	}
}

// A typo in a service definition stops the booth at startup, rather than
// surfacing as a dead countdown in front of the first paying customer.
func TestBadURLsAreRefusedAtStartup(t *testing.T) {
	for _, bad := range []string{"", "localhost:5513", "ftp://box/fire", "://nope"} {
		if _, err := shutter.New(bad, time.Second); err == nil {
			t.Errorf("started with an unusable shutter url %q", bad)
		}
	}
}
