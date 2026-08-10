package gcal

import (
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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// One key for the whole package. Generating an RSA key is the slowest thing in
// these tests by a wide margin and none of them care which key it is.
var testKey = sync.OnceValue(func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
})

// credentialsFor builds the base64 service-account key file an operator pastes
// into the environment, so the tests exercise the real credential path rather
// than reaching past it.
func credentialsFor(t *testing.T, tokenURI string) string {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(testKey())
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	raw, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"client_email": "booking@bykami.iam.gserviceaccount.com",
		"private_key":  string(keyPEM),
		"token_uri":    tokenURI,
	})
	if err != nil {
		t.Fatalf("marshal credentials: %v", err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// google is a stand-in for the two Google endpoints this package talks to. It
// verifies the JWT the way Google does, so a signing mistake fails here rather
// than in production.
type google struct {
	srv *httptest.Server

	tokens   atomic.Int64
	freeBusy atomic.Int64

	mu           sync.Mutex
	busy         []Range
	notShared    bool
	tokenRefuses bool
	lastEvent    map[string]any
	deleteStatus int
}

func newGoogle(t *testing.T) *google {
	t.Helper()
	g := &google{deleteStatus: http.StatusNoContent}

	mux := http.NewServeMux()

	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		g.tokens.Add(1)
		g.mu.Lock()
		refuses := g.tokenRefuses
		g.mu.Unlock()

		if refuses {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			// Google's real shape: "error" is a string here, not an object.
			w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid JWT Signature."}`))
			return
		}

		if err := r.ParseForm(); err != nil {
			t.Errorf("token request body: %v", err)
		}
		if got := r.PostForm.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			t.Errorf("grant_type = %q", got)
		}
		if err := verifyAssertion(r.PostForm.Get("assertion"), g.srv.URL+"/token"); err != nil {
			t.Errorf("assertion: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"tok_live","expires_in":3599,"token_type":"Bearer"}`))
	})

	mux.HandleFunc("POST /freeBusy", func(w http.ResponseWriter, r *http.Request) {
		g.freeBusy.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer tok_live" {
			t.Errorf("Authorization = %q, want the minted token", got)
		}

		var req struct {
			TimeMin string `json:"timeMin"`
			TimeMax string `json:"timeMax"`
			Items   []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("freeBusy body: %v", err)
		}
		if len(req.Items) != 1 {
			t.Fatalf("freeBusy asked about %d calendars, want 1", len(req.Items))
		}
		if _, err := time.Parse(time.RFC3339, req.TimeMin); err != nil {
			t.Errorf("timeMin %q is not RFC3339", req.TimeMin)
		}
		id := req.Items[0].ID

		g.mu.Lock()
		defer g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if g.notShared {
			// What a calendar that was never shared with the service account
			// actually returns: HTTP 200, with the failure inside the body.
			json.NewEncoder(w).Encode(map[string]any{
				"calendars": map[string]any{
					id: map[string]any{
						"errors": []map[string]string{{"domain": "global", "reason": "notFound"}},
					},
				},
			})
			return
		}
		busy := make([]map[string]string, 0, len(g.busy))
		for _, b := range g.busy {
			busy = append(busy, map[string]string{
				"start": b.StartsAt.Format(time.RFC3339),
				"end":   b.EndsAt.Format(time.RFC3339),
			})
		}
		json.NewEncoder(w).Encode(map[string]any{
			"calendars": map[string]any{id: map[string]any{"busy": busy}},
		})
	})

	mux.HandleFunc("POST /calendars/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("event body: %v", err)
		}
		g.mu.Lock()
		g.lastEvent = body
		g.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"evt_written"}`))
	})

	mux.HandleFunc("DELETE /calendars/{id}/events/{event}", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		status := g.deleteStatus
		g.mu.Unlock()
		w.WriteHeader(status)
	})

	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

// client points a real Client at the fake, through New, so the credential
// decoding and key parsing are part of every test.
func (g *google) client(t *testing.T) *Client {
	t.Helper()
	c, err := New(credentialsFor(t, g.srv.URL+"/token"))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if c == nil {
		t.Fatal("new returned no client for a valid credential")
	}
	c.Endpoints(g.srv.URL, g.srv.URL+"/token")
	return c
}

// verifyAssertion checks the JWT the way the token endpoint would.
func verifyAssertion(assertion, audience string) error {
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		return errors.New("not three dot-separated parts")
	}

	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return err
	}
	if header.Alg != "RS256" || header.Typ != "JWT" {
		return errors.New("header is not an RS256 JWT")
	}

	var claims struct {
		Iss   string `json:"iss"`
		Scope string `json:"scope"`
		Aud   string `json:"aud"`
		Iat   int64  `json:"iat"`
		Exp   int64  `json:"exp"`
	}
	raw, err = base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return err
	}
	if claims.Iss != "booking@bykami.iam.gserviceaccount.com" {
		return errors.New("iss is not the service account")
	}
	if claims.Scope != scope {
		return errors.New("scope is not the calendar scope")
	}
	// A mismatched audience is rejected by Google with a message that does not say
	// so, which makes it worth asserting here.
	if claims.Aud != audience {
		return errors.New("aud is not the token endpoint")
	}
	if claims.Exp <= claims.Iat {
		return errors.New("exp is not after iat")
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return rsa.VerifyPKCS1v15(&testKey().PublicKey, crypto.SHA256, digest[:], sig)
}

func TestNoCredentialsMeansNoClient(t *testing.T) {
	// A box nobody has connected a calendar to is the ordinary state, not a
	// misconfiguration, and booking has to keep working on it.
	for _, in := range []string{"", "   ", "\n"} {
		c, err := New(in)
		if err != nil {
			t.Errorf("New(%q) errored: %v", in, err)
		}
		if c != nil {
			t.Errorf("New(%q) returned a client", in)
		}
	}
}

func TestBadCredentialsAreRefusedAtStartup(t *testing.T) {
	der, _ := x509.MarshalPKCS8PrivateKey(testKey())
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	b64 := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.StdEncoding.EncodeToString(raw)
	}

	tests := []struct {
		name string
		in   string
	}{
		{"not base64", "this is not base64 %%%"},
		{"not json", base64.StdEncoding.EncodeToString([]byte("hello"))},
		{"no client_email", b64(map[string]string{"private_key": keyPEM})},
		{"no private_key", b64(map[string]string{"client_email": "a@b.com"})},
		{"key is not pem", b64(map[string]string{"client_email": "a@b.com", "private_key": "nope"})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Refused now, loudly, rather than at the first booking of the day.
			if _, err := New(tc.in); err == nil {
				t.Error("accepted a credential it cannot use")
			}
		})
	}
}

func TestFreeBusyReadsWhatTheOwnerBlocked(t *testing.T) {
	g := newGoogle(t)
	from := time.Date(2026, 8, 12, 7, 0, 0, 0, time.UTC)
	g.busy = []Range{{StartsAt: from, EndsAt: from.Add(time.Hour)}}

	got, err := g.client(t).FreeBusy(context.Background(), "studio@group.calendar.google.com",
		from.Add(-time.Hour), from.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("freeBusy: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("read %d busy ranges, want 1", len(got))
	}
	if !got[0].StartsAt.Equal(from) || !got[0].EndsAt.Equal(from.Add(time.Hour)) {
		t.Errorf("range = %v, want %v to %v", got[0], from, from.Add(time.Hour))
	}
}

func TestFreeBusySaysWhenTheCalendarWasNeverShared(t *testing.T) {
	g := newGoogle(t)
	g.notShared = true

	_, err := g.client(t).FreeBusy(context.Background(), "studio@group.calendar.google.com",
		time.Now(), time.Now().Add(time.Hour))
	if err == nil {
		t.Fatal("a calendar that is not shared read as empty rather than broken")
	}
	// This is the mistake every first deployment makes, and it arrives as HTTP 200
	// with the failure buried in the body. The message has to name the fix.
	if !strings.Contains(err.Error(), "booking@bykami.iam.gserviceaccount.com") {
		t.Errorf("error does not say who to share the calendar with: %v", err)
	}
	if !strings.Contains(err.Error(), "notFound") {
		t.Errorf("error lost Google's reason: %v", err)
	}
}

func TestOneTokenServesManyCalls(t *testing.T) {
	g := newGoogle(t)
	c := g.client(t)
	ctx := context.Background()

	for range 3 {
		if _, err := c.FreeBusy(ctx, "cal", time.Now(), time.Now().Add(time.Hour)); err != nil {
			t.Fatalf("freeBusy: %v", err)
		}
	}
	// An access token is good for an hour. Minting one per call would be three
	// round trips of pure waste on every poll.
	if got := g.tokens.Load(); got != 1 {
		t.Errorf("minted %d tokens for 3 calls, want 1", got)
	}
	if got := g.freeBusy.Load(); got != 3 {
		t.Errorf("made %d freeBusy calls, want 3", got)
	}
}

func TestARefusedTokenSaysWhy(t *testing.T) {
	g := newGoogle(t)
	g.tokenRefuses = true

	_, err := g.client(t).FreeBusy(context.Background(), "cal", time.Now(), time.Now().Add(time.Hour))
	if err == nil {
		t.Fatal("a refused token was not an error")
	}
	// The token endpoint's envelope puts a bare string under "error" where the
	// Calendar API puts an object. Decoding only the second shape turns this into
	// "400 Bad Request" and costs somebody an afternoon.
	if !strings.Contains(err.Error(), "Invalid JWT Signature") {
		t.Errorf("error lost Google's explanation: %v", err)
	}
}

func TestInsertWritesTheEventAndReturnsItsID(t *testing.T) {
	g := newGoogle(t)
	start := time.Date(2026, 8, 12, 14, 0, 0, 0, time.UTC)

	id, err := g.client(t).Insert(context.Background(), "studio@group.calendar.google.com", Event{
		Summary:     "Y2K · Rina (3 orang)",
		Description: "Kode booking: abc",
		Location:    "Jajag",
		StartsAt:    start,
		EndsAt:      start.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id != "evt_written" {
		t.Errorf("event id = %q, want evt_written", id)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if got := g.lastEvent["summary"]; got != "Y2K · Rina (3 orang)" {
		t.Errorf("summary = %v", got)
	}
	// Google needs the time under a "dateTime" key, not as a bare string. Getting
	// this wrong is a 400 that says nothing useful.
	when, ok := g.lastEvent["start"].(map[string]any)
	if !ok {
		t.Fatalf("start = %#v, want an object with dateTime", g.lastEvent["start"])
	}
	if _, err := time.Parse(time.RFC3339, when["dateTime"].(string)); err != nil {
		t.Errorf("start.dateTime %v is not RFC3339", when["dateTime"])
	}
}

func TestDeletingAnEventThatIsAlreadyGoneIsDone(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{"deleted", http.StatusNoContent, nil},
		{"already deleted", http.StatusGone, ErrGone},
		{"never existed", http.StatusNotFound, ErrGone},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := newGoogle(t)
			g.deleteStatus = tc.status

			err := g.client(t).Delete(context.Background(), "cal", "evt_1")
			if tc.want == nil && err != nil {
				t.Fatalf("delete: %v", err)
			}
			// An event the owner already removed by hand is the state the delete was
			// trying to reach. Treating it as a failure retries it forever.
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}
