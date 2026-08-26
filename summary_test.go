package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func seedCount(t *testing.T, s *Store, app, event, day string, count int64) {
	t.Helper()
	if _, err := s.Increment(app, event, day, count); err != nil {
		t.Fatalf("seed %s/%s/%s: %v", app, event, day, err)
	}
}

func at(t *testing.T, stamp string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("parse %q: %v", stamp, err)
	}
	return parsed
}

// The acceptance test: counters seeded across several apps, events and days must
// come back as accurate per-app x event totals for the window asked about.
func TestSummarizeAggregatesAcrossAppsEventsAndDays(t *testing.T) {
	store := newTestStore(t)

	// Window of 5 days at 2026-07-23 starts on 2026-07-19.
	seedCount(t, store, "solaris", "widget.open", "2026-07-19", 2)
	seedCount(t, store, "solaris", "widget.open", "2026-07-21", 3)
	seedCount(t, store, "solaris", "widget.open", "2026-07-23", 5)
	seedCount(t, store, "solaris", "widget.tasks.compose", "2026-07-22", 7)
	seedCount(t, store, "photos", "album.view", "2026-07-20", 11)
	seedCount(t, store, "photos", "album.view", "2026-07-23", 1)
	seedCount(t, store, "photos", "upload.done", "2026-07-21", 4)
	// Outside the window: must not be counted anywhere.
	seedCount(t, store, "solaris", "widget.open", "2026-07-18", 100)
	seedCount(t, store, "recipes", "recipe.open", "2026-07-01", 50)

	got, err := summarize(store, at(t, "2026-07-23T09:00:00Z"), 5)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}

	want := summary{
		Days:  5,
		From:  "2026-07-19",
		Total: 33,
		Apps: []appTotals{
			{App: "photos", Total: 16, Events: []eventTotal{
				{Event: "album.view", Count: 12},
				{Event: "upload.done", Count: 4},
			}},
			{App: "solaris", Total: 17, Events: []eventTotal{
				{Event: "widget.open", Count: 10},
				{Event: "widget.tasks.compose", Count: 7},
			}},
		},
		KnownApps: []string{"photos", "recipes", "solaris"},
	}
	assertSummary(t, got, want)
}

// The window has to be the same one retention uses, in both directions: the day
// exactly on the boundary is in the readout, the day before it is not. If these
// two ever drift, a summary shows a day retention has already deleted, or hides
// one it kept.
func TestSummarizeAgreesWithRetentionOnTheBoundaryDay(t *testing.T) {
	const days = 5
	now := at(t, "2026-07-23T23:30:00Z")
	start := windowStart(now, days) // 2026-07-19

	store := newTestStore(t)
	seedCount(t, store, "solaris", "widget.open", "2026-07-18", 9) // one day older
	seedCount(t, store, "solaris", "widget.open", start, 4)        // exactly on the boundary

	got, err := summarize(store, now, days)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if got.From != start {
		t.Fatalf("summary starts at %q, want the shared window start %q", got.From, start)
	}
	if got.Total != 4 {
		t.Fatalf("total = %d, want 4 — the boundary day must be in and the day before it out", got.Total)
	}

	// And the same store, swept by retention with the same window, keeps exactly
	// the days the summary reported on.
	r := newRetainer(store, days)
	r.now = func() time.Time { return now }
	if _, err := r.sweep(); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if want := []string{start}; !equalDays(storedDays(t, store), want) {
		t.Fatalf("retention kept %v, want %v — the two windows disagree", storedDays(t, store), want)
	}

	after, err := summarize(store, now, days)
	if err != nil {
		t.Fatalf("summarize after sweep: %v", err)
	}
	if after.Total != got.Total {
		t.Fatalf("total changed from %d to %d after a sweep — the summary was counting rows retention drops", got.Total, after.Total)
	}
}

func TestSummarizeWindowSlidesWithToday(t *testing.T) {
	store := newTestStore(t)
	seedCount(t, store, "solaris", "widget.open", "2026-07-19", 4)
	seedCount(t, store, "solaris", "widget.open", "2026-07-23", 1)

	got, err := summarize(store, at(t, "2026-07-24T00:00:01Z"), 5) // window now starts 2026-07-20
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if got.From != "2026-07-20" || got.Total != 1 {
		t.Fatalf("summarize a day later = from %q total %d, want from 2026-07-20 total 1", got.From, got.Total)
	}
}

// "Nothing has ever reported" and "this app reported, just not this window" are
// different answers and a reader must be able to tell them apart.
func TestSummarizeDistinguishesNoDataFromNoActivity(t *testing.T) {
	store := newTestStore(t)

	empty, err := summarize(store, at(t, "2026-07-23T09:00:00Z"), 5)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if len(empty.Apps) != 0 || len(empty.KnownApps) != 0 || empty.Total != 0 {
		t.Fatalf("empty store = %+v, want no apps, no known apps, total 0", empty)
	}

	seedCount(t, store, "solaris", "widget.open", "2026-07-01", 12)
	quiet, err := summarize(store, at(t, "2026-07-23T09:00:00Z"), 5)
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if len(quiet.Apps) != 0 || quiet.Total != 0 {
		t.Fatalf("apps = %+v, want none — nothing was counted inside the window", quiet.Apps)
	}
	if len(quiet.KnownApps) != 1 || quiet.KnownApps[0] != "solaris" {
		t.Fatalf("knownApps = %v, want [solaris] — a silent app must still be visible", quiet.KnownApps)
	}
}

func TestSummarizeFailsOnAClosedStore(t *testing.T) {
	store := newTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := summarize(store, time.Now(), 5); err == nil {
		t.Fatal("expected an error summarizing a closed store")
	}
}

func TestSummaryDaysParsing(t *testing.T) {
	if got, err := summaryDays("", 90); err != nil || got != 90 {
		t.Fatalf("summaryDays(\"\", 90) = %d, %v; want 90, nil", got, err)
	}
	if got, err := summaryDays("7", 90); err != nil || got != 7 {
		t.Fatalf("summaryDays(\"7\", 90) = %d, %v; want 7, nil", got, err)
	}
	if got, err := summaryDays("3650", 90); err != nil || got != maxSummaryDays {
		t.Fatalf("summaryDays(\"3650\", 90) = %d, %v; want %d, nil", got, err, maxSummaryDays)
	}
	for _, raw := range []string{"0", "-1", "abc", "7.5", "3651", " 7", "99999999999999999999"} {
		t.Run(raw, func(t *testing.T) {
			if got, err := summaryDays(raw, 90); err == nil {
				t.Fatalf("summaryDays(%q) = %d, nil; want an error", raw, got)
			}
		})
	}
}

func getSummary(t *testing.T, h http.Handler, target string) (*httptest.ResponseRecorder, summary) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))

	var body summary
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s: %v (body %s)", target, err, rec.Body.String())
		}
	}
	return rec, body
}

func TestSummaryHandlerServesTheWindowAsked(t *testing.T) {
	store := newTestStore(t)
	seedCount(t, store, "solaris", "widget.open", "2026-07-19", 2)
	seedCount(t, store, "solaris", "widget.open", "2026-07-23", 3)

	handler := &summaryHandler{store: store, defaultDays: 90, now: func() time.Time {
		return at(t, "2026-07-23T09:00:00Z")
	}}

	rec, body := getSummary(t, handler, "/summary?days=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /summary?days=2 = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}
	if body.Days != 2 || body.From != "2026-07-22" || body.Total != 3 {
		t.Fatalf("body = %+v, want the 2-day window from 2026-07-22 totalling 3", body)
	}

	// No ?days= falls back to the retention window, which covers both rows.
	if _, all := getSummary(t, handler, "/summary"); all.Days != 90 || all.Total != 5 {
		t.Fatalf("default window = %+v, want 90 days totalling 5", all)
	}
}

// An empty readout must still be a well-formed one: JSON arrays, never null,
// so a caller can index into them without a nil check.
func TestSummaryHandlerRendersEmptyArraysNotNull(t *testing.T) {
	handler := &summaryHandler{store: newTestStore(t), defaultDays: 90, now: time.Now}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/summary", nil))

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, field := range []string{"apps", "knownApps"} {
		if got := string(raw[field]); got != "[]" {
			t.Fatalf("%s = %s, want []", field, got)
		}
	}
}

func TestSummaryHandlerRejectsBadRequests(t *testing.T) {
	handler := &summaryHandler{store: newTestStore(t), defaultDays: 90, now: time.Now}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/summary", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /summary = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", got)
	}

	rec, _ = getSummary(t, handler, "/summary?days=nope")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("GET /summary?days=nope = %d, want 400", rec.Code)
	}
}

func TestSummaryHandlerSurfacesAStoreFailure(t *testing.T) {
	store := newTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	handler := &summaryHandler{store: store, defaultDays: 90, now: time.Now}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/summary", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /summary on a broken store = %d, want 500", rec.Code)
	}
}

func assertSummary(t *testing.T, got, want summary) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("summary =\n%s\nwant\n%s", gotJSON, wantJSON)
	}
}
