package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	defaultRetentionDays = 90
	retentionInterval    = 24 * time.Hour
)

// retainer drops counter rows that fall outside the retention window.
//
// It deletes rather than rolls up: a rolled-up total is still a record of
// activity from a day we promised to forget, so "fully deletable" means the
// rows are gone, with nothing derived left behind.
type retainer struct {
	store *Store
	days  int
	now   func() time.Time

	mu        sync.Mutex
	lastSweep time.Time
	lastErr   error
}

func newRetainer(store *Store, days int) *retainer {
	return &retainer{store: store, days: days, now: time.Now}
}

// retentionDays reads the window from the environment. A malformed value is an
// error rather than a fall-back to the default: a typo must not quietly widen
// how long we keep data.
func retentionDays() (int, error) {
	raw := os.Getenv("USAGE_METRICS_RETENTION_DAYS")
	if raw == "" {
		return defaultRetentionDays, nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("USAGE_METRICS_RETENTION_DAYS=%q is not an integer", raw)
	}
	if days < 1 {
		return 0, fmt.Errorf("USAGE_METRICS_RETENTION_DAYS=%d must be at least 1 day", days)
	}
	return days, nil
}

// sweep deletes everything older than the window and returns how many rows went.
// It is a pure function of (today, days), so running it twice in a row deletes
// nothing the second time.
func (r *retainer) sweep() (int64, error) {
	deleted, err := r.store.DeleteBefore(windowStart(r.now(), r.days))

	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastErr = err
	if err == nil {
		r.lastSweep = r.now()
	}
	return deleted, err
}

// run sweeps on every tick until ctx is cancelled. A failing sweep logs and
// keeps the loop alive; health() is what makes the failure visible outside.
func (r *retainer) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if deleted, err := r.sweep(); err != nil {
				log.Printf("retention: sweep failed: %v", err)
			} else if deleted > 0 {
				log.Printf("retention: dropped %d counter rows older than %d days", deleted, r.days)
			}
		}
	}
}

// health reports why retention is not doing its job — a failed sweep, or a loop
// that has gone quiet for longer than staleAfter because its goroutine died.
func (r *retainer) health(staleAfter time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.lastErr != nil {
		return fmt.Errorf("last retention sweep failed: %w", r.lastErr)
	}
	if r.lastSweep.IsZero() {
		return errors.New("retention has not swept yet")
	}
	if age := r.now().Sub(r.lastSweep); age > staleAfter {
		return fmt.Errorf("last retention sweep was %s ago", age.Round(time.Second))
	}
	return nil
}
