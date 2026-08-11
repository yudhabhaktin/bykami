package main

import (
	"net"
	"net/http"
)

// Which hostname serves what.
//
// One binary on one port answers for two surfaces that have nothing to do with
// each other: a public API that booths and the marketing sites call, and an
// operator console. Splitting them by hostname rather than by path means the
// console is not reachable at all on the address the booths know, so a bug in
// its routing cannot expose an operator page to the public API's callers.
//
// # Why anything unrecognised gets both
//
// The obvious rule — admin gets the console, app gets the API, everything else
// gets a 404 — breaks the deploy. `roles/app` gates a release on
// `http://127.0.0.1:8080/healthz` and bykami-update.sh polls the same address
// before it accepts a new binary, so a Host of "127.0.0.1:8080" has to reach
// the API or every deploy fails and every auto-update rolls back. `astro dev`
// against a local binary is the same case.
//
// So the split is enforced only for the two names it is *about*. That is not a
// hole: those names exist only at Cloudflare's edge, the tunnel is the only
// route to this process, and the box has no inbound port open. Nothing on the
// public internet can present a Host this function has not been told about.
const (
	adminHost = "admin.bykami.id"
	apiHost   = "app.bykami.id"
)

// byHost routes a request by the name it was asked for.
//
// console answers on adminHost, api on apiHost, and both on anything else —
// which is what local, health-check and development traffic gets, and is the
// behaviour every caller had before this split existed.
func byHost(console, api http.Handler, both http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch hostname(r.Host) {
		case adminHost:
			console.ServeHTTP(w, r)
		case apiHost:
			api.ServeHTTP(w, r)
		default:
			both.ServeHTTP(w, r)
		}
	})
}

// hostname is r.Host without any port.
//
// net.SplitHostPort is an error for a bare hostname, which is the ordinary case
// behind the tunnel — Cloudflare forwards "admin.bykami.id" and not
// "admin.bykami.id:443" — so the error means "no port to strip" rather than a
// malformed value.
func hostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}
