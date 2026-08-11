package gcal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Connecting a calendar without a shell on the server.
//
// The package doc explains why the ongoing traffic is a service account and not
// OAuth: a refresh token from an unverified project dies after seven days, and
// the failure is a booking page that quietly stops updating. None of that
// changes here. What this file adds is a way to perform the *one manual step* a
// service account still needs — the owner sharing a calendar with its address —
// from the console instead of from Google Calendar's settings screen.
//
// So the consent is spent immediately and thrown away. The operator signs in,
// the app reads their calendar list, and for each calendar they map it writes
// an ACL rule granting the service account "writer" — exactly the rule the
// manual share creates. Then the token goes in the bin. Nothing is stored, so
// there is nothing to expire, nothing to refresh, and nothing worth stealing
// out of the database.
//
// That is also why the consent asks for online access only. A refresh token is
// not requested at all, which is the strongest form of "we do not keep this":
// Google never issues one, so no amount of later carelessness can leak it.

// DefaultAuthURL is Google's consent screen.
const DefaultAuthURL = "https://accounts.google.com/o/oauth2/v2/auth"

// ErrNotOwned means the signed-in account cannot change who may see a calendar,
// which is what happens when somebody connects a calendar that was shared with
// them rather than one they own.
var ErrNotOwned = errors.New("gcal: this account does not own that calendar")

// Calendar is one entry from the signed-in account's calendar list.
type Calendar struct {
	ID   string
	Name string
	// Primary is the account's own default calendar, which is the one an owner
	// who has never made a second calendar will be looking for.
	Primary bool
	// Owned reports whether this account may grant access to it. Calendars
	// shared *to* the account appear in the same list and cannot be granted on,
	// so offering them would produce a failure at the last step.
	Owned bool
}

// Connect runs the consent flow. Nil when no OAuth client is configured, which
// is the ordinary state of a deployment that connects calendars by hand.
type Connect struct {
	clientID     string
	clientSecret string

	authURL  string
	tokenURL string
	base     string
	client   *http.Client
}

// NewConnect returns nil when either half of the client credential is missing.
// Both come from a Google Cloud OAuth client of type "Web application"; the
// redirect URI is supplied per request, because it is the console's own address
// and this package does not know it.
func NewConnect(clientID, clientSecret string) *Connect {
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	if clientID == "" || clientSecret == "" {
		return nil
	}
	return &Connect{
		clientID:     clientID,
		clientSecret: clientSecret,
		authURL:      DefaultAuthURL,
		tokenURL:     DefaultTokenURL,
		base:         DefaultBase,
		client:       &http.Client{Timeout: 30 * time.Second},
	}
}

// Endpoints redirects this client at a test server.
func (c *Connect) Endpoints(authURL, tokenURL, base string) {
	c.authURL = authURL
	c.tokenURL = tokenURL
	c.base = strings.TrimSuffix(base, "/")
}

// AuthCodeURL is where the operator's browser is sent to consent.
//
// No access_type=offline and no prompt=consent: this flow wants an access token
// good for the next few minutes and explicitly does not want a refresh token.
func (c *Connect) AuthCodeURL(redirectURI, state string) string {
	q := url.Values{
		"client_id":     {c.clientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {scope},
		"state":         {state},
		// The account chooser, every time. Two Google accounts are being
		// connected here and they own different booths; landing on whichever one
		// the browser happens to be signed in to is how the wrong calendar gets
		// attached to the wrong booth.
		"prompt": {"select_account"},
	}
	return c.authURL + "?" + q.Encode()
}

// Exchange turns the callback's code into an access token. The token is the
// caller's to hold for the few minutes the mapping takes and then drop.
func (c *Connect) Exchange(ctx context.Context, code, redirectURI string) (string, error) {
	form := url.Values{
		"code":          {code},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("gcal: exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("gcal: exchange: %w", err)
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return "", fmt.Errorf("gcal: read exchange: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return "", fmt.Errorf("gcal: exchange: %s", apiError(raw, res.Status))
	}

	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("gcal: decode exchange: %w", err)
	}
	if out.AccessToken == "" {
		return "", errors.New("gcal: exchange returned no access_token")
	}
	return out.AccessToken, nil
}

// Calendars lists what the signed-in account can offer.
//
// Only calendars it owns are returned as Owned; the rest are still listed so an
// operator who expected to see one can tell it apart from a calendar that is
// missing entirely, which is a different mistake with a different fix.
func (c *Connect) Calendars(ctx context.Context, token string) ([]Calendar, error) {
	var out struct {
		Items []struct {
			ID         string `json:"id"`
			Summary    string `json:"summary"`
			Primary    bool   `json:"primary"`
			AccessRole string `json:"accessRole"`
			Deleted    bool   `json:"deleted"`
		} `json:"items"`
	}
	if err := c.call(ctx, token, http.MethodGet, "/users/me/calendarList", nil, &out); err != nil {
		return nil, err
	}

	cals := make([]Calendar, 0, len(out.Items))
	for _, it := range out.Items {
		if it.Deleted {
			continue
		}
		cals = append(cals, Calendar{
			ID:      it.ID,
			Name:    it.Summary,
			Primary: it.Primary,
			Owned:   it.AccessRole == "owner",
		})
	}
	return cals, nil
}

// Share grants the service account permission to read and write a calendar.
//
// This is the whole point of the flow: it is the same grant an owner makes by
// hand in Google Calendar under "Share with specific people", set to "Make
// changes to events". Writer rather than reader because the studio's bookings
// are mirrored into the calendar as events, not just read out of it.
//
// Idempotent at Google's end — inserting a rule for an address that already has
// one updates it, so an operator who connects the same calendar twice gets the
// same result rather than an error they have to interpret.
func (c *Connect) Share(ctx context.Context, token, calendarID, grantee string) error {
	body := map[string]any{
		"role":  "writer",
		"scope": map[string]string{"type": "user", "value": grantee},
	}
	err := c.call(ctx, token, http.MethodPost,
		"/calendars/"+url.PathEscape(calendarID)+"/acl", body, nil)
	if err == nil {
		return nil
	}
	// 403 here is one specific mistake and worth naming: the account is signed
	// in and the calendar exists, but it belongs to somebody else, so this
	// account cannot change who may see it.
	if strings.Contains(err.Error(), "403") || strings.Contains(strings.ToLower(err.Error()), "forbidden") {
		return fmt.Errorf("%w: %s", ErrNotOwned, calendarID)
	}
	return err
}

func (c *Connect) call(ctx context.Context, token, method, path string, body, out any) error {
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
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("gcal: %s %s: %d %s", method, path, res.StatusCode, apiError(raw, res.Status))
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("gcal: decode response: %w", err)
	}
	return nil
}
