package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

// defaultDBPath sits under the mounted volume the template provides, not the
// container fs, which is wiped on every pod recreate.
const defaultDBPath = "/data/usage-metrics.db"

const addr = ":8080"

func main() {
	path := dbPath()

	handler, stop, err := newService(path)
	if err != nil {
		log.Fatal(err)
	}
	defer stop()

	log.Printf("usage-metrics listening on %s, counters at %s", addr, path)
	log.Fatal(http.ListenAndServe(addr, handler))
}

// newService opens the store, forgets whatever has fallen out of the retention
// window, starts the sweep loop, and returns the routes plus a stop that unwinds
// all of it.
func newService(path string) (http.Handler, func(), error) {
	token, err := serviceToken()
	if err != nil {
		return nil, nil, err
	}

	days, err := retentionDays()
	if err != nil {
		return nil, nil, err
	}

	store, err := OpenStore(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open store at %s: %w", path, err)
	}

	// Sweep before serving, so a box that was off for a month forgets what it
	// owes before it takes another write.
	retention := newRetainer(store, days)
	if deleted, err := retention.sweep(); err != nil {
		log.Printf("retention: initial sweep failed: %v", err)
	} else {
		log.Printf("retention: keeping %d days, dropped %d stale counter rows", days, deleted)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go retention.run(ctx, retentionInterval)

	stop := func() {
		cancel()
		store.Close()
	}
	return newMux(store, retention, token), stop, nil
}

func dbPath() string {
	if path := os.Getenv("USAGE_METRICS_DB_PATH"); path != "" {
		return path
	}
	return defaultDBPath
}

func newMux(store *Store, retention *retainer, token string) *http.ServeMux {
	mux := http.NewServeMux()
	// /healthz is the one open route: ServiceBay polls it from the host and its
	// probe cannot carry a header, and all it discloses is whether retention is
	// still running. Both counter routes are gated — /summary reads household
	// activity, which is exactly what the token exists to keep off the LAN.
	mux.Handle("/healthz", &healthHandler{retention: retention})
	mux.Handle("/ingest", requireToken(token, &ingestHandler{store: store}))
	mux.Handle("/summary", requireToken(token, &summaryHandler{store: store, defaultDays: retention.days, now: time.Now}))
	return mux
}

type healthHandler struct {
	retention *retainer
}

// A retention loop that has stopped is a broken privacy promise, so it turns the
// health check ServiceBay watches red instead of going quiet.
func (h *healthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h.retention.health(2 * retentionInterval); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}
