package main

import (
	"log"
	"net/http"
	"os"
)

// defaultDBPath sits under the mounted volume the template provides, not the
// container fs, which is wiped on every pod recreate.
const defaultDBPath = "/data/usage-metrics.db"

const addr = ":8080"

func main() {
	path := dbPath()

	store, err := OpenStore(path)
	if err != nil {
		log.Fatalf("open store at %s: %v", path, err)
	}
	defer store.Close()

	log.Printf("usage-metrics listening on %s, counters at %s", addr, path)
	if err := http.ListenAndServe(addr, newMux(store)); err != nil {
		log.Fatal(err)
	}
}

func dbPath() string {
	if path := os.Getenv("USAGE_METRICS_DB_PATH"); path != "" {
		return path
	}
	return defaultDBPath
}

func newMux(store *Store) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	mux.Handle("/ingest", &ingestHandler{store: store})
	return mux
}

func healthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}
