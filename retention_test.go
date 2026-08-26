package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(t *testing.T, stamp string) *fakeClock {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("parse %q: %v", stamp, err)
	}
	return &fakeClock{t: parsed}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func seed(t *testing.T, s *Store, days ...string) {
	t.Helper()
	for _, day := range days {
		if _, err := s.Increment("solaris", "widget.open", day, 1); err != nil {
			t.Fatalf("seed %s: %v", day, err)
		}
	}
}

// storedDays reads back what the database actually holds. Retention is asserted
// against this, never against the delete statement's own row count — "deleted 2
// rows" is a claim, the remaining rows are the evidence.
func storedDays(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.Query("SELECT day FROM counters ORDER BY day")
	if err != nil {
		t.Fatalf("read days: %v", err)
	}
	defer rows.Close()

	var days []string
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			t.Fatalf("scan day: %v", err)
		}
		days = append(days, day)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read days: %v", err)
	}
	return days
}

func equalDays(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestWindowStartCountsTodayAsTheFirstDayOfTheWindow(t *testing.T) {
	cases := []struct {
		name string
		now  string
		days int
		want string
	}{
		{"a one-day window keeps only today", "2026-07-23T12:00:00Z", 1, "2026-07-23"},
		{"a three-day window keeps today and two before it", "2026-07-23T12:00:00Z", 3, "2026-07-21"},
		{"the default window reaches back 89 days", "2026-07-23T12:00:00Z", defaultRetentionDays, "2026-04-25"},
		{"the window crosses a month boundary", "2026-03-02T00:00:01Z", 3, "2026-02-28"},
		{"the day is the UTC day, not the local one", "2026-07-24T10:00:00+14:00", 1, "2026-07-23"},
		{"late in the UTC day is still that day", "2026-07-23T23:59:59Z", 2, "2026-07-22"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, tc.now)
			if err != nil {
				t.Fatalf("parse now: %v", err)
			}
			if got := windowStart(now, tc.days); got != tc.want {
				t.Fatalf("windowStart(%s, %d) = %q, want %q", tc.now, tc.days, got, tc.want)
			}
		})
	}
}

// The boundary row — the day exactly on the cutoff — must survive; the one day
// older than it must not. An off-by-one here silently drops a day of real data.
func TestSweepDropsOnlyCountersOlderThanTheWindow(t *testing.T) {
	store := newTestStore(t)
	clock := newClock(t, "2026-07-23T23:30:00Z")

	// Window of 5 days at 2026-07-23 → cutoff 2026-07-19.
	seed(t, store,
		"2026-07-17", // well outside
		"2026-07-18", // one day before the cutoff
		"2026-07-19", // exactly on the cutoff
		"2026-07-20",
		"2026-07-23", // today
		"2026-07-24", // dated ahead of us by a skewed caller
	)

	r := newRetainer(store, 5)
	r.now = clock.now

	deleted, err := r.sweep()
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("sweep deleted %d rows, want 2", deleted)
	}

	want := []string{"2026-07-19", "2026-07-20", "2026-07-23", "2026-07-24"}
	if got := storedDays(t, store); !equalDays(got, want) {
		t.Fatalf("surviving days = %v, want %v", got, want)
	}

	// Genuinely gone, not hidden behind a flag: no row of any shape is left for
	// the dropped days.
	for _, day := range []string{"2026-07-17", "2026-07-18"} {
		var rows int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM counters WHERE day = ?", day).Scan(&rows); err != nil {
			t.Fatalf("count %s: %v", day, err)
		}
		if rows != 0 {
			t.Fatalf("%s still has %d rows after the sweep", day, rows)
		}
	}
}

func TestSweepIsIdempotentAndTracksTheMovingWindow(t *testing.T) {
	store := newTestStore(t)
	clock := newClock(t, "2026-07-23T08:00:00Z")
	seed(t, store, "2026-07-19", "2026-07-20", "2026-07-21", "2026-07-22", "2026-07-23")

	r := newRetainer(store, 3) // cutoff 2026-07-21
	r.now = clock.now

	if deleted, err := r.sweep(); err != nil || deleted != 2 {
		t.Fatalf("first sweep = %d, %v; want 2, nil", deleted, err)
	}
	afterFirst := storedDays(t, store)

	// Same day, same window: nothing more may go.
	if deleted, err := r.sweep(); err != nil || deleted != 0 {
		t.Fatalf("second sweep = %d, %v; want 0, nil", deleted, err)
	}
	if deleted, err := r.sweep(); err != nil || deleted != 0 {
		t.Fatalf("third sweep = %d, %v; want 0, nil", deleted, err)
	}
	if got := storedDays(t, store); !equalDays(got, afterFirst) {
		t.Fatalf("re-running the sweep changed the store: %v, was %v", got, afterFirst)
	}

	// A day later the window slides by exactly one day — no more, no less.
	clock.advance(24 * time.Hour)
	if deleted, err := r.sweep(); err != nil || deleted != 1 {
		t.Fatalf("sweep after a day = %d, %v; want 1, nil", deleted, err)
	}
	want := []string{"2026-07-22", "2026-07-23"}
	if got := storedDays(t, store); !equalDays(got, want) {
		t.Fatalf("surviving days = %v, want %v", got, want)
	}
}

func TestSweepOnAnEmptyStoreDeletesNothing(t *testing.T) {
	r := newRetainer(newTestStore(t), defaultRetentionDays)
	if deleted, err := r.sweep(); err != nil || deleted != 0 {
		t.Fatalf("sweep = %d, %v; want 0, nil", deleted, err)
	}
}

func TestRetentionDaysFromTheEnvironment(t *testing.T) {
	t.Setenv("USAGE_METRICS_RETENTION_DAYS", "")
	if got, err := retentionDays(); err != nil || got != defaultRetentionDays {
		t.Fatalf("retentionDays() = %d, %v; want %d, nil", got, err, defaultRetentionDays)
	}

	t.Setenv("USAGE_METRICS_RETENTION_DAYS", "30")
	if got, err := retentionDays(); err != nil || got != 30 {
		t.Fatalf("retentionDays() = %d, %v; want 30, nil", got, err)
	}

	// A bad value must not fall back to the default — that would quietly keep
	// data for longer than the operator asked.
	for _, raw := range []string{"abc", "0", "-5", "30.5", "90d", " 90"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("USAGE_METRICS_RETENTION_DAYS", raw)
			if got, err := retentionDays(); err == nil {
				t.Fatalf("retentionDays() = %d, nil; want an error for %q", got, raw)
			}
		})
	}
}

func TestSweepSurfacesAStoreFailure(t *testing.T) {
	store := newTestStore(t)
	r := newRetainer(store, defaultRetentionDays)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := r.sweep(); err == nil {
		t.Fatal("expected an error sweeping a closed store")
	}
	if err := r.health(retentionInterval); err == nil {
		t.Fatal("a failed sweep must show up in health()")
	}
}

func TestHealthTracksTheSweepLoop(t *testing.T) {
	store := newTestStore(t)
	clock := newClock(t, "2026-07-23T08:00:00Z")
	r := newRetainer(store, defaultRetentionDays)
	r.now = clock.now

	if err := r.health(2 * retentionInterval); err == nil {
		t.Fatal("before the first sweep, health() must not report healthy")
	}

	if _, err := r.sweep(); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if err := r.health(2 * retentionInterval); err != nil {
		t.Fatalf("health() after a fresh sweep = %v, want nil", err)
	}

	// A loop that stopped ticking has to be visible from outside the process.
	clock.advance(3 * retentionInterval)
	if err := r.health(2 * retentionInterval); err == nil {
		t.Fatal("a stale sweep must be reported as unhealthy")
	}
}

func TestRunSweepsOnEveryTickAndStopsWithTheContext(t *testing.T) {
	store := newTestStore(t)
	clock := newClock(t, "2026-07-23T08:00:00Z")
	seed(t, store, "2026-07-01", "2026-07-23")

	r := newRetainer(store, 3)
	r.now = clock.now

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.run(ctx, time.Millisecond)
	}()

	waitFor(t, func() bool { return equalDays(storedDays(t, store), []string{"2026-07-23"}) },
		"the ticker never dropped the out-of-window day")

	// A later tick picks up a row that has since fallen out of the window.
	seed(t, store, "2026-07-02")
	waitFor(t, func() bool { return equalDays(storedDays(t, store), []string{"2026-07-23"}) },
		"a later tick never dropped a newly stale row")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return after its context was cancelled")
	}
}

func TestRunKeepsTickingAfterAFailedSweep(t *testing.T) {
	store := newTestStore(t)
	r := newRetainer(store, defaultRetentionDays)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.run(ctx, time.Millisecond)
	}()

	waitFor(t, func() bool { return lastSweepErr(r) != nil },
		"a failing sweep never reached health()")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run stopped surviving its own errors")
	}
}

func lastSweepErr(r *retainer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastErr
}

func waitFor(t *testing.T, ok func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(message)
}
