package main

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"os"
	"strings"
)

// serviceToken reads the deploy-time bearer token that gates the counter
// endpoints. An unset value is a startup error, not "auth off": the callers are
// other services' backends, so a deploy that lost the variable has to fail
// loudly rather than quietly serve an open write endpoint on the box.
func serviceToken() (string, error) {
	token := os.Getenv("USAGE_METRICS_TOKEN")
	if token == "" {
		return "", errors.New("USAGE_METRICS_TOKEN must be set: /ingest and /summary are token-gated and will not be served without one")
	}
	return token, nil
}

// requireToken gates a handler on the service token. Callers present it as
// `Authorization: Bearer <token>` — there is no SSO here on purpose: the
// principals are other services, not a resident with a browser session.
func requireToken(token string, next http.Handler) http.Handler {
	return &tokenGate{token: token, next: next}
}

type tokenGate struct {
	token string
	next  http.Handler
}

func (g *tokenGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !presentsToken(r, g.token) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="usage-metrics"`)
		writeError(w, http.StatusUnauthorized, "a valid bearer token is required")
		return
	}
	g.next.ServeHTTP(w, r)
}

func presentsToken(r *http.Request, token string) bool {
	header := r.Header.Get("Authorization")
	const scheme = "bearer "
	if len(header) < len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return false
	}
	// Constant time, so a wrong token cannot be walked byte by byte from response timings.
	return subtle.ConstantTimeCompare([]byte(header[len(scheme):]), []byte(token)) == 1
}
