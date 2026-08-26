package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sweptRetainer(t *testing.T, store *Store) *retainer {
	t.Helper()
	r := newRetainer(store, defaultRetentionDays)
	if _, err := r.sweep(); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	return r
}

func TestHealthz(t *testing.T) {
	store := newTestStore(t)
	handler := &healthHandler{retention: sweptRetainer(t, store)}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("expected body %q, got %q", "ok", rec.Body.String())
	}
}

// The install gate must go red when retention stops running — a service that
// keeps ingesting while it has stopped forgetting is not healthy.
func TestHealthzFailsWhenRetentionIsNotRunning(t *testing.T) {
	handler := &healthHandler{retention: newRetainer(newTestStore(t), defaultRetentionDays)}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 while retention has never swept, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "retention") {
		t.Fatalf("expected the body to name retention, got %q", rec.Body.String())
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
	store := newTestStore(t)
	mux := newMux(store, sweptRetainer(t, store))

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

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/summary", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/summary = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
}

// The boot path: a service coming up on an existing database forgets what has
// fallen out of the window before it serves anything.
func TestNewServiceSweepsBeforeServing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "counters.db")
	today := time.Now().UTC().Format(time.DateOnly)

	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	seed(t, store, "2020-01-01", today)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	handler, stop, err := newService(path)
	if err != nil {
		t.Fatalf("newService: %v", err)
	}
	defer stop()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200; body %s", rec.Code, rec.Body.String())
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })
	if got := storedDays(t, reopened); !equalDays(got, []string{today}) {
		t.Fatalf("days after boot = %v, want %v", got, []string{today})
	}
}

func TestNewServiceRefusesToStartOnBadConfig(t *testing.T) {
	t.Setenv("USAGE_METRICS_RETENTION_DAYS", "forever")
	if _, _, err := newService(filepath.Join(t.TempDir(), "counters.db")); err == nil {
		t.Fatal("expected a bad retention window to stop the service from starting")
	}

	t.Setenv("USAGE_METRICS_RETENTION_DAYS", "")
	if _, _, err := newService(filepath.Join(t.TempDir(), "missing-dir", "counters.db")); err == nil {
		t.Fatal("expected an unopenable store to stop the service from starting")
	}
}
