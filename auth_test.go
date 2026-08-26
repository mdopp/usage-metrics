package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

const testToken = "sb-test-service-token"

func gatedMux(t *testing.T) http.Handler {
	t.Helper()
	store := newTestStore(t)
	return newMux(store, sweptRetainer(t, store), testToken)
}

// The acceptance case from #5: a caller with no token cannot write a counter.
func TestIngestRejectsAnUnauthenticatedCall(t *testing.T) {
	mux := gatedMux(t)

	req := httptest.NewRequest(http.MethodPost, "/ingest",
		strings.NewReader(`{"app":"solaris","event":"widget.open","day":"2026-07-23","count":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("un-authenticated /ingest = %d, want 401; body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer ") {
		t.Fatalf("WWW-Authenticate = %q, want a Bearer challenge", got)
	}
}

func TestGatedRoutesRejectWrongAndMalformedCredentials(t *testing.T) {
	mux := gatedMux(t)

	cases := map[string]string{
		"wrong token":    "Bearer not-the-token",
		"token prefix":   "Bearer " + testToken[:len(testToken)-1],
		"wrong scheme":   "Basic " + testToken,
		"bare token":     testToken,
		"empty header":   "",
		"scheme only":    "Bearer ",
		"leading spaces": "  Bearer " + testToken,
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/summary", nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("/summary with %q = %d, want 401", header, rec.Code)
			}
		})
	}
}

// The scheme is case-insensitive per RFC 7235, so a caller writing "bearer"
// gets through rather than a confusing 401.
func TestGateAcceptsTheTokenWhateverTheSchemeCasing(t *testing.T) {
	mux := gatedMux(t)

	for _, scheme := range []string{"Bearer", "bearer", "BEARER"} {
		req := httptest.NewRequest(http.MethodGet, "/summary", nil)
		req.Header.Set("Authorization", scheme+" "+testToken)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("/summary with %q scheme = %d, want 200", scheme, rec.Code)
		}
	}
}

// ServiceBay's health poller probes from the host and cannot attach a header,
// so the install gate must answer an un-authenticated request.
func TestHealthzStaysOpen(t *testing.T) {
	mux := gatedMux(t)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200 without a token", rec.Code)
	}
}

// Fail closed: no token in the environment must stop the service from starting
// rather than serve the counter endpoints open.
func TestNewServiceRefusesToStartWithoutAToken(t *testing.T) {
	t.Setenv("USAGE_METRICS_TOKEN", "")

	_, _, err := newService(filepath.Join(t.TempDir(), "counters.db"))
	if err == nil {
		t.Fatal("expected a missing USAGE_METRICS_TOKEN to stop the service from starting")
	}
	if !strings.Contains(err.Error(), "USAGE_METRICS_TOKEN") {
		t.Fatalf("error = %v, want it to name the variable", err)
	}
}
