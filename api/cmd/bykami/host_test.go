package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The three surfaces, each answering with its own name so a test can tell which
// one a request reached.
func namedHandler(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(name))
	})
}

func TestHostnameRoutingKeepsTheTwoSurfacesApart(t *testing.T) {
	h := byHost(namedHandler("console"), namedHandler("api"), namedHandler("both"))

	for _, tc := range []struct {
		host string
		want string
		why  string
	}{
		{"admin.bykami.id", "console", "the operator console's own name"},
		{"app.bykami.id", "api", "the name booths and the sites call"},

		// Cloudflare forwards the bare name, but a port must not change the
		// answer — a request that arrives as admin.bykami.id:443 is the same
		// request.
		{"admin.bykami.id:443", "console", "the console with an explicit port"},
		{"app.bykami.id:443", "api", "the API with an explicit port"},

		// The deploy gate and bykami-update.sh both poll this exact address. If
		// it stops reaching the API, every deploy fails and every auto-update
		// rolls back — which is why unrecognised names get the combined mux
		// rather than a 404.
		{"127.0.0.1:8080", "both", "the deploy's health probe"},
		{"localhost:8080", "both", "a developer's local binary"},
		{"", "both", "a request with no Host at all"},

		// A name nobody routed here. Still the combined mux: the tunnel is the
		// only way in, so this cannot arrive from the public internet.
		{"booth-test.bykami.id", "both", "another hostname on the same tunnel"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		r.Host = tc.host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if got := w.Body.String(); got != tc.want {
			t.Errorf("%s (%s) reached %q, want %q", tc.host, tc.why, got, tc.want)
		}
	}
}

func TestHostnameStripsThePort(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"admin.bykami.id", "admin.bykami.id"},
		{"admin.bykami.id:443", "admin.bykami.id"},
		{"127.0.0.1:8080", "127.0.0.1"},
		{"", ""},
		// IPv6 carries colons of its own, so a naive split on ":" would mangle
		// it into something that matches nothing.
		{"[::1]:8080", "::1"},
	} {
		if got := hostname(tc.in); got != tc.want {
			t.Errorf("hostname(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
