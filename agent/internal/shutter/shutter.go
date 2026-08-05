// Package shutter fires a camera the agent does not own.
//
// The capture design keeps the camera at arm's length on purpose: a vendor tool
// tethers over USB and drops JPEGs into a hot folder, and the agent watches the
// folder. That decoupling is what keeps cgo and a vendor SDK out of the build,
// and none of it is given up here — releasing the shutter is one HTTP request
// to whatever already owns the camera. digiCamControl serves exactly this on
// localhost:5513, so the booth gains a trigger without gaining a driver.
//
// design/kiosk.md recommends a USB relay into the RS-60E3 jack instead, and
// that is still the sturdier answer: its failure mode is a plug coming out,
// which is visible and costs a few dollars to fix, rather than a vendor update
// quietly changing an API. This exists because the relay is hardware that has
// to be bought and wired, and the booth without either makes a customer press
// the camera by hand.
package shutter

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout bounds one shutter release.
//
// Generous, because the request does not always return the instant the mirror
// moves — some tools answer only once the frame has transferred — and a booth
// that gave up at two seconds would report a failure for photographs it had
// successfully taken. Bounded all the same: a countdown that has ended has a
// customer standing in front of it, and the one thing that must not happen is
// the screen hanging with no explanation.
const DefaultTimeout = 15 * time.Second

// maxReply is how much of the answer is read. These tools reply with a word;
// anything longer is a diagnostic page, and it is going into a log line.
const maxReply = 4 << 10

// okReply is what a tool that succeeded says. See Fire for why the body is
// read at all.
const okReply = "OK"

// Release fires one camera, over HTTP.
type Release struct {
	url    string
	client *http.Client
}

// New builds a release from the URL the booth was started with.
//
// The URL is parsed here rather than at the first shutter press so that a
// typo in a service definition stops the booth from starting, instead of
// surfacing as a dead countdown in front of the first paying customer.
func New(rawURL string, timeout time.Duration) (*Release, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("shutter url %q: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("shutter url %q: want an http:// or https:// address", rawURL)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("shutter url %q: no host to call", rawURL)
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Release{url: rawURL, client: &http.Client{Timeout: timeout}}, nil
}

// Fire releases the shutter once.
//
// It reports failure loudly, and that is the whole point of the type: a booth
// that counted down and then did not photograph anybody has taken money and
// produced nothing, and it must say so rather than move on to the next pose.
func (r *Release) Fire(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return err
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("shutter: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxReply))
	reply := firstLine(strings.TrimSpace(string(body)))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("shutter: %s: %s", resp.Status, reply)
	}

	// The body is read because the status code is not the whole answer.
	// digiCamControl replies 200 and puts the failure in the text — "no camera
	// connected" arrives as a perfectly successful HTTP response — so a booth
	// trusting the status alone would count down, fire nothing, and report
	// that everything went well.
	//
	// Anything that is neither empty nor "OK" is therefore treated as a
	// refusal. That errs towards a false alarm on some future tool that answers
	// 200 with a chatty body, and that is the right way round: a booth that
	// wrongly says "call staff" is annoying, and one that wrongly says nothing
	// is wrong sends people home without their photographs.
	if reply != "" && !strings.EqualFold(reply, okReply) {
		return fmt.Errorf("shutter refused: %s", reply)
	}
	return nil
}

// firstLine keeps an error readable when the far end returns a web page.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
