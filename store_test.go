package main

import (
	"path/filepath"
	"sync"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "counters.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func total(t *testing.T, s *Store, app, event, day string) int64 {
	t.Helper()
	var count int64
	err := s.db.QueryRow(
		"SELECT count FROM counters WHERE app = ? AND event = ? AND day = ?", app, event, day,
	).Scan(&count)
	if err != nil {
		t.Fatalf("read counter %s/%s/%s: %v", app, event, day, err)
	}
	return count
}

func rowCount(t *testing.T, s *Store) int {
	t.Helper()
	var rows int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM counters").Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return rows
}

func TestOpenStoreUsesWAL(t *testing.T) {
	mode, err := newTestStore(t).journalMode()
	if err != nil {
		t.Fatalf("journalMode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("expected journal_mode wal, got %q", mode)
	}
}

func TestIncrementAccumulatesPerKey(t *testing.T) {
	store := newTestStore(t)

	if got, err := store.Increment("solaris", "widget.tasks.compose", "2026-07-23", 1); err != nil || got != 1 {
		t.Fatalf("first increment = %d, %v; want 1, nil", got, err)
	}
	if got, err := store.Increment("solaris", "widget.tasks.compose", "2026-07-23", 4); err != nil || got != 5 {
		t.Fatalf("second increment = %d, %v; want 5, nil", got, err)
	}
	if _, err := store.Increment("solaris", "widget.tasks.compose", "2026-07-24", 2); err != nil {
		t.Fatalf("other day: %v", err)
	}
	if _, err := store.Increment("photos", "widget.tasks.compose", "2026-07-23", 7); err != nil {
		t.Fatalf("other app: %v", err)
	}

	if got := total(t, store, "solaris", "widget.tasks.compose", "2026-07-23"); got != 5 {
		t.Fatalf("counter = %d, want 5", got)
	}
	if got := rowCount(t, store); got != 3 {
		t.Fatalf("row count = %d, want 3 (one per app/event/day)", got)
	}
}

func TestIncrementSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "counters.db")

	first, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if _, err := first.Increment("solaris", "widget.open", "2026-07-23", 3); err != nil {
		t.Fatalf("Increment: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { second.Close() })

	if got := total(t, second, "solaris", "widget.open", "2026-07-23"); got != 3 {
		t.Fatalf("counter after reopen = %d, want 3", got)
	}
}

// The whole point of WAL + busy_timeout: parallel writers must all land, and
// none of them may be lost to a "database is locked" error.
func TestIncrementUnderConcurrentWriters(t *testing.T) {
	store := newTestStore(t)

	const writers, perWriter = 32, 25

	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	start := make(chan struct{})

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < perWriter; i++ {
				if _, err := store.Increment("solaris", "widget.open", "2026-07-23", 1); err != nil {
					errs <- err
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent Increment: %v", err)
	}
	if got := total(t, store, "solaris", "widget.open", "2026-07-23"); got != writers*perWriter {
		t.Fatalf("counter = %d, want %d — increments were lost under concurrency", got, writers*perWriter)
	}
}

func TestOpenStoreFailsOnAnUnwritablePath(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "missing-dir", "counters.db"))
	if err == nil {
		store.Close()
		t.Fatal("expected an error when the mount directory does not exist")
	}
}

func TestIncrementFailsOnAClosedStore(t *testing.T) {
	store := newTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := store.Increment("solaris", "widget.open", "2026-07-23", 1); err == nil {
		t.Fatal("expected an error from a closed store")
	}
}
