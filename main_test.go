package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	healthz(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("expected body %q, got %q", "ok", rec.Body.String())
	}
}

func TestDBPathDefaultsToTheMountedVolume(t *testing.T) {
	t.Setenv("USAGE_METRICS_DB_PATH", "")
	if got := dbPath(); got != defaultDBPath {
		t.Fatalf("dbPath() = %q, want %q", got, defaultDBPath)
	}

	t.Setenv("USAGE_METRICS_DB_PATH", "/mnt/elsewhere/counters.db")
	if got := dbPath(); got != "/mnt/elsewhere/counters.db" {
		t.Fatalf("dbPath() = %q, want the override", got)
	}
}

func TestMuxRoutes(t *testing.T) {
	mux := newMux(newTestStore(t))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/ingest",
		strings.NewReader(`{"app":"solaris","event":"widget.open","day":"2026-07-23","count":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/ingest = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
}
