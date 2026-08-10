// Package gcal talks to Google Calendar: what the studio is busy with, and what
// it has just sold.
//
// The owner works out of Google Calendar and did so before this platform
// existed. YouCanBook.me wrote bookings into it, and the owner blocks their own
// time there by hand. Replacing the booking tool without replacing that habit is
// the whole requirement, so this package reads freeBusy and writes events, and
// nothing else.
//
// # Why a service account
//
// The obvious route is OAuth: send the owner to a consent screen, keep a refresh
// token, present it forever. It is also a trap. A Google Cloud project that has
// not been through verification issues refresh tokens that expire in seven days,
// and the Calendar scope is one Google treats as sensitive, so verification is
// not a formality. The failure mode is a booking page that works for a week and
// then quietly stops updating, weeks before anybody connects the two.
//
// A service account has no such clock. It authenticates by signing a JWT with a
// key that does not rotate, and a consumer Gmail account can share a calendar
// with its address the same way it would with a colleague. The one-time manual
// step is that share — set to "Make changes to events" — and there is no consent
// screen and no token to lose.
//
// # Why no dependency
//
// google.golang.org/api would be the largest thing in this module by an order of
// magnitude, and CI enforces a tidy go.sum. What is actually needed is a signed
// JWT, a token exchange and three REST calls, all of which the standard library
// does. The shape follows internal/instagram: nil when unconfigured, a bounded
// client, and a base URL a test can point somewhere else.
package gcal

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultBase is the Calendar v3 endpoint. Overridable so tests can point at
	// an httptest server.
	DefaultBase = "https://www.googleapis.com/calendar/v3"
	// DefaultTokenURL is where a signed JWT is exchanged for an access token.
	DefaultTokenURL = "https://oauth2.googleapis.com/token"

	scope = "https://www.googleapis.com/auth/calendar"

	// Access tokens last an hour. Renewing a minute early avoids losing a request
	// to a token that expired in flight.
	tokenSkew = 1 * time.Minute
	maxBody   = 4 << 20
)

// ErrGone means the event is already absent, which for a deletion is success.
var ErrGone = errors.New("gcal: event no longer exists")

// Range is an interval a calendar reports as busy.
type Range struct {
	StartsAt time.Time
	EndsAt   time.Time
}

// Event is what gets written into the owner's calendar.
type Event struct {
	Summary     string
	Description string
	Location    string
	StartsAt    time.Time
	EndsAt      time.Time
}

// Client is an authenticated Calendar caller.
type Client struct {
	email string
	key   *rsa.PrivateKey

	base     string
	tokenURL string
	client   *http.Client

	// The access token is shared by every caller and refreshed by whichever one
	// finds it stale. Guarded because the availability poller and the booking
	// mirror both use this concurrently.
	mu      sync.Mutex
	token   string
	expires time.Time
}

// credentials is the part of a Google service-account key file this needs.
type credentials struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

// New builds a client from a base64-encoded service-account key file.
//
// Base64 because the key is PEM, PEM is multi-line, and the credential reaches
// the process through a systemd EnvironmentFile, which has no syntax for a value
// containing newlines. Encoding the whole JSON file rather than just the key also
// means one variable instead of two, and `base64 < key.json` is the entire
// operator instruction.
//
// Returns (nil, nil) when the credential is empty. Nil rather than an error,
// because no calendar connected is the ordinary state of a deployment nobody has
// connected yet, and booking has to keep working from its own database when it is
// — see internal/booking on the busy cache.
func New(encoded string) (*Client, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, nil
	}

	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("gcal: credentials are not base64: %w", err)
	}
	var creds credentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, fmt.Errorf("gcal: credentials are not a service account key file: %w", err)
	}
	if creds.ClientEmail == "" || creds.PrivateKey == "" {
		return nil, errors.New("gcal: credentials are missing client_email or private_key")
	}

	key, err := parseKey(creds.PrivateKey)
	if err != nil {
		return nil, err
	}
	tokenURL := creds.TokenURI
	if tokenURL == "" {
		tokenURL = DefaultTokenURL
	}

	return &Client{
		email:    creds.ClientEmail,
		key:      key,
		base:     DefaultBase,
		tokenURL: tokenURL,
		// Bounded. A stalled call must not hold a goroutine until the process
		// restarts, and this one runs on a box with a gigabyte to its name.
		client: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Email is the address the studio's calendars have to be shared with. Surfaced
// so the operator console can print it rather than making somebody open a JSON
// file to find out why nothing is syncing.
func (c *Client) Email() string { return c.email }

// Endpoints redirects this client at a test server.
func (c *Client) Endpoints(base, tokenURL string) {
	c.base = strings.TrimSuffix(base, "/")
	c.tokenURL = tokenURL
}

func parseKey(pemKey string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, errors.New("gcal: private_key is not PEM")
	}
	// Google issues PKCS#8. PKCS#1 is accepted too because a key rotated by hand
	// through openssl can come back in either, and the failure would otherwise
	// arrive as an unexplained parse error at startup.
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("gcal: private_key is %T, want RSA", key)
		}
		return rsaKey, nil
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("gcal: private_key is not an RSA key: %w", err)
	}
	return key, nil
}

// FreeBusy reports when a calendar is occupied.
//
// Used rather than listing events because it is the narrower permission and the
// smaller answer: the studio needs to know that 14:00 is taken, not who the
// owner is meeting. A calendar that has not been shared with the service account
// comes back as an error inside the response body rather than an HTTP status, so
// that case is unpacked explicitly — it is the single most likely
// misconfiguration and deserves to say so.
func (c *Client) FreeBusy(ctx context.Context, calendarID string, from, to time.Time) ([]Range, error) {
	body := map[string]any{
		"timeMin": from.Format(time.RFC3339),
		"timeMax": to.Format(time.RFC3339),
		"items":   []map[string]string{{"id": calendarID}},
	}

	var out struct {
		Calendars map[string]struct {
			Busy []struct {
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"busy"`
			Errors []struct {
				Domain string `json:"domain"`
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"calendars"`
	}
	if err := c.do(ctx, http.MethodPost, "/freeBusy", body, &out); err != nil {
		return nil, err
	}

	cal, ok := out.Calendars[calendarID]
	if !ok {
		return nil, fmt.Errorf("gcal: no answer for calendar %q", calendarID)
	}
	if len(cal.Errors) > 0 {
		return nil, fmt.Errorf("gcal: calendar %q: %s — check it is shared with %s",
			calendarID, cal.Errors[0].Reason, c.email)
	}

	out2 := make([]Range, 0, len(cal.Busy))
	for _, b := range cal.Busy {
		start, err := time.Parse(time.RFC3339, b.Start)
		if err != nil {
			return nil, fmt.Errorf("gcal: busy start %q: %w", b.Start, err)
		}
		end, err := time.Parse(time.RFC3339, b.End)
		if err != nil {
			return nil, fmt.Errorf("gcal: busy end %q: %w", b.End, err)
		}
		out2 = append(out2, Range{StartsAt: start.UTC(), EndsAt: end.UTC()})
	}
	return out2, nil
}

// Insert writes an event and returns its id.
func (c *Client) Insert(ctx context.Context, calendarID string, e Event) (string, error) {
	body := map[string]any{
		"summary":     e.Summary,
		"description": e.Description,
		"location":    e.Location,
		"start":       map[string]string{"dateTime": e.StartsAt.Format(time.RFC3339)},
		"end":         map[string]string{"dateTime": e.EndsAt.Format(time.RFC3339)},
	}
	var out struct {
		ID string `json:"id"`
	}
	path := "/calendars/" + url.PathEscape(calendarID) + "/events"
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("gcal: calendar accepted the event but returned no id")
	}
	return out.ID, nil
}

// Delete removes an event. An event that is already gone reports ErrGone, which
// callers should treat as done rather than retry.
func (c *Client) Delete(ctx context.Context, calendarID, eventID string) error {
	path := "/calendars/" + url.PathEscape(calendarID) + "/events/" + url.PathEscape(eventID)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("gcal: encode request: %w", err)
		}
		payload = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, payload)
	if err != nil {
		return fmt.Errorf("gcal: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("gcal: %s %s: %w", method, path, err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return fmt.Errorf("gcal: read response: %w", err)
	}

	switch {
	// 410 is a deletion that had already happened, and 404 on a delete is the
	// same thing said differently. Both are the desired state.
	case res.StatusCode == http.StatusGone,
		res.StatusCode == http.StatusNotFound && method == http.MethodDelete:
		return ErrGone
	case res.StatusCode == http.StatusNoContent:
		return nil
	case res.StatusCode < 200 || res.StatusCode > 299:
		return fmt.Errorf("gcal: %s %s: %s", method, path, apiError(raw, res.Status))
	}

	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("gcal: decode response: %w", err)
	}
	return nil
}

// accessToken returns a live token, minting one if the current one is close to
// expiry.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Now().Before(c.expires.Add(-tokenSkew)) {
		return c.token, nil
	}

	assertion, err := c.assertion()
	if err != nil {
		return "", err
	}

	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("gcal: token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("gcal: token: %w", err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return "", fmt.Errorf("gcal: read token: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return "", fmt.Errorf("gcal: token: %s", apiError(raw, res.Status))
	}

	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("gcal: decode token: %w", err)
	}
	if out.AccessToken == "" {
		return "", errors.New("gcal: token response carried no access_token")
	}

	c.token = out.AccessToken
	c.expires = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	return c.token, nil
}

// assertion builds and signs the JWT that stands in for a password.
func (c *Client) assertion() (string, error) {
	now := time.Now()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iss":   c.email,
		"scope": scope,
		"aud":   c.tokenURL,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}

	encode := func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(raw), nil
	}

	h, err := encode(header)
	if err != nil {
		return "", fmt.Errorf("gcal: encode jwt header: %w", err)
	}
	p, err := encode(claims)
	if err != nil {
		return "", fmt.Errorf("gcal: encode jwt claims: %w", err)
	}

	signing := h + "." + p
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("gcal: sign jwt: %w", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// apiError pulls the message out of Google's error envelope, falling back to the
// HTTP status.
//
// The envelope is where the useful part is: "notFound" against a calendar id
// means it was never shared, and "Invalid JWT Signature" means the key is wrong.
// A bare "400 Bad Request" says neither, and both are afternoon-sized mistakes to
// diagnose without the sentence.
//
// Two shapes, because the two endpoints disagree. The Calendar API nests an
// object under "error"; the OAuth token endpoint puts a bare string there and the
// detail in "error_description". Decoding is attempted separately for each rather
// than with one struct, because a struct expecting an object fails outright on the
// string and would discard the message it was written to surface.
func apiError(raw []byte, status string) string {
	var api struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &api); err == nil && api.Error.Message != "" {
		return api.Error.Message
	}

	var oauth struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &oauth); err == nil {
		switch {
		case oauth.Error != "" && oauth.Description != "":
			return oauth.Error + ": " + oauth.Description
		case oauth.Error != "":
			return oauth.Error
		}
	}
	return status
}
