package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func postIngest(t *testing.T, h http.Handler, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIngestPersistsIncrement(t *testing.T) {
	store := newTestStore(t)
	handler := &ingestHandler{store: store}

	body := `{"app":"solaris","event":"widget.tasks.compose","day":"2026-07-23","count":2}`
	rec := postIngest(t, handler, "application/json", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}

	var got ingestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	want := ingestResponse{App: "solaris", Event: "widget.tasks.compose", Day: "2026-07-23", Total: 2}
	if got != want {
		t.Fatalf("response = %+v, want %+v", got, want)
	}

	postIngest(t, handler, "application/json; charset=utf-8", body)
	if n := total(t, store, "solaris", "widget.tasks.compose", "2026-07-23"); n != 4 {
		t.Fatalf("counter = %d, want 4", n)
	}
}

func TestIngestRejectsMalformedBodies(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty body", ``},
		{"not json", `not json at all`},
		{"not an object", `[{"app":"solaris","event":"e","day":"2026-07-23","count":1}]`},
		{"two objects", `{"app":"a","event":"e","day":"2026-07-23","count":1}{"app":"a","event":"e","day":"2026-07-23","count":1}`},
		{"unknown field", `{"app":"a","event":"e","day":"2026-07-23","count":1,"uid":"michael"}`},
		{"free-form payload", `{"app":"a","event":"e","day":"2026-07-23","count":1,"payload":{"note":"bought milk"}}`},
		{"missing app", `{"event":"e","day":"2026-07-23","count":1}`},
		{"missing event", `{"app":"a","day":"2026-07-23","count":1}`},
		{"missing day", `{"app":"a","event":"e","count":1}`},
		{"missing count", `{"app":"a","event":"e","day":"2026-07-23"}`},
		{"zero count", `{"app":"a","event":"e","day":"2026-07-23","count":0}`},
		{"negative count", `{"app":"a","event":"e","day":"2026-07-23","count":-5}`},
		{"count not a number", `{"app":"a","event":"e","day":"2026-07-23","count":"1"}`},
		{"uppercase app", `{"app":"Solaris","event":"e","day":"2026-07-23","count":1}`},
		{"app with spaces", `{"app":"my app","event":"e","day":"2026-07-23","count":1}`},
		{"app too long", `{"app":"` + strings.Repeat("a", 65) + `","event":"e","day":"2026-07-23","count":1}`},
		{"event with slash", `{"app":"a","event":"widget/open","day":"2026-07-23","count":1}`},
		{"day not a date", `{"app":"a","event":"e","day":"yesterday","count":1}`},
		{"day unpadded", `{"app":"a","event":"e","day":"2026-7-3","count":1}`},
		{"day is a timestamp", `{"app":"a","event":"e","day":"2026-07-23T10:00:00Z","count":1}`},
		{"day out of range", `{"app":"a","event":"e","day":"2026-13-40","count":1}`},
		{"oversized body", `{"app":"a","event":"` + strings.Repeat("x", 5000) + `","day":"2026-07-23","count":1}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStore(t)
			rec := postIngest(t, &ingestHandler{store: store}, "application/json", tc.body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
			}
			var errBody map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil || errBody["error"] == "" {
				t.Fatalf("expected a JSON error explaining the rejection, got %q", rec.Body.String())
			}
			if rows := rowCount(t, store); rows != 0 {
				t.Fatalf("a rejected request wrote %d rows; it must change nothing", rows)
			}
		})
	}
}

func TestIngestRejectsWrongMethodAndContentType(t *testing.T) {
	store := newTestStore(t)
	handler := &ingestHandler{store: store}
	body := `{"app":"a","event":"e","day":"2026-07-23","count":1}`

	req := httptest.NewRequest(http.MethodGet, "/ingest", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow = %q, want POST", allow)
	}

	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		if rec := postIngest(t, handler, contentType, body); rec.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("content-type %q: status = %d, want 415", contentType, rec.Code)
		}
	}

	if rows := rowCount(t, store); rows != 0 {
		t.Fatalf("rejected requests wrote %d rows", rows)
	}
}

// Concurrent callers hitting the same (app, event, day) must sum, not clobber.
func TestIngestConcurrentRequestsAllLand(t *testing.T) {
	store := newTestStore(t)
	handler := &ingestHandler{store: store}

	const callers, perCaller = 24, 20
	body := `{"app":"solaris","event":"widget.open","day":"2026-07-23","count":1}`

	var wg sync.WaitGroup
	statuses := make(chan int, callers*perCaller)
	start := make(chan struct{})

	for c := 0; c < callers; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < perCaller; i++ {
				req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				statuses <- rec.Code
			}
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)

	for code := range statuses {
		if code != http.StatusOK {
			t.Fatalf("concurrent ingest returned %d", code)
		}
	}
	if got := total(t, store, "solaris", "widget.open", "2026-07-23"); got != callers*perCaller {
		t.Fatalf("counter = %d, want %d", got, callers*perCaller)
	}
	if rows := rowCount(t, store); rows != 1 {
		t.Fatalf("row count = %d, want 1", rows)
	}
}

// A write that failed must not answer as though it succeeded.
func TestIngestReportsAStoreFailure(t *testing.T) {
	store := newTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rec := postIngest(t, &ingestHandler{store: store}, "application/json",
		`{"app":"solaris","event":"widget.open","day":"2026-07-23","count":1}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %s", rec.Code, rec.Body.String())
	}
}
