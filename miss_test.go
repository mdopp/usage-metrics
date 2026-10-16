package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The misses tests run at 2026-10-16: the 30-day window then starts 2026-09-17.
const missNow = "2026-10-16T21:00:00Z"

func missRows(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM misses").Scan(&n); err != nil {
		t.Fatalf("count misses: %v", err)
	}
	return n
}

// missColumns is the evidence for "nothing about who": the table has exactly
// these columns and no other.
func missColumns(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.Query("SELECT name FROM pragma_table_info('misses') ORDER BY cid")
	if err != nil {
		t.Fatalf("table info: %v", err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan: %v", err)
		}
		cols = append(cols, c)
	}
	return cols
}

func postMiss(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/miss", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func missBody(source, text, day string) string {
	b, _ := json.Marshal(map[string]string{"source": source, "text": text, "day": day})
	return string(b)
}

func getMisses(t *testing.T, h http.Handler, target string) (*httptest.ResponseRecorder, missList) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	var list missList
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
	}
	return rec, list
}

func missHandlers(t *testing.T) (*Store, http.Handler, http.Handler, *fakeClock) {
	t.Helper()
	store := newTestStore(t)
	clock := newClock(t, missNow)
	return store, &missHandler{store: store, now: clock.now}, &missesHandler{store: store, now: clock.now}, clock
}

// The acceptance case: a sentence the Bote could not answer is posted, and the
// admin's readout lists it — the same sentence twice counts 2, not two rows.
func TestMissShowsUpInMissesAndCountsUp(t *testing.T) {
	store, post, list, _ := missHandlers(t)

	for want := int64(1); want <= 2; want++ {
		rec := postMiss(t, post, missBody("bote", "Wann kommt der Müll?", "2026-10-16"))
		if rec.Code != http.StatusOK {
			t.Fatalf("POST /miss = %d, body %s", rec.Code, rec.Body.String())
		}
		var got missResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got != (missResponse{Source: "bote", Day: "2026-10-16", Count: want}) {
			t.Fatalf("response = %+v, want count %d", got, want)
		}
	}
	postMiss(t, post, missBody("stimme", "Mach das Licht im Keller an", "2026-10-01"))

	if n := missRows(t, store); n != 2 {
		t.Fatalf("stored %d rows, want 2 (one per sentence and day)", n)
	}
	if cols := missColumns(t, store); strings.Join(cols, ",") != "source,text,day,count" {
		t.Fatalf("misses columns = %v, want exactly source,text,day,count", cols)
	}

	rec, got := getMisses(t, list, "/misses")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /misses = %d, body %s", rec.Code, rec.Body.String())
	}
	want := missList{Days: 30, From: "2026-09-17", Total: 3, Misses: []missTotal{
		{Source: "bote", Text: "Wann kommt der Müll?", Count: 2, LastDay: "2026-10-16"},
		{Source: "stimme", Text: "Mach das Licht im Keller an", Count: 1, LastDay: "2026-10-01"},
	}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("/misses = %+v\nwant      %+v", got, want)
	}
}

func TestMissTrimsAndAcceptsTheLongestSentence(t *testing.T) {
	store, post, list, _ := missHandlers(t)

	if rec := postMiss(t, post, missBody("bote", "  Wann kommt der Müll?  ", "2026-10-16")); rec.Code != http.StatusOK {
		t.Fatalf("padded sentence = %d, body %s", rec.Code, rec.Body.String())
	}
	longest := strings.Repeat("ü", maxMissRunes)
	if rec := postMiss(t, post, missBody("bote", longest, "2026-09-17")); rec.Code != http.StatusOK {
		t.Fatalf("%d-rune sentence on the window's first day = %d, body %s", maxMissRunes, rec.Code, rec.Body.String())
	}

	_, got := getMisses(t, list, "/misses")
	texts := map[string]bool{}
	for _, m := range got.Misses {
		texts[m.Text] = true
	}
	if !texts["Wann kommt der Müll?"] || !texts[longest] || len(texts) != 2 {
		t.Fatalf("stored texts = %v, want the trimmed sentence and the longest one", texts)
	}
	if n := missRows(t, store); n != 2 {
		t.Fatalf("stored %d rows, want 2", n)
	}
}

// Strict like /ingest: anything that is not exactly a sentence, a source and a
// day in the window is refused and nothing is stored — above all a field that
// would say who asked.
func TestMissRejectsEverythingButASentenceSourceAndDay(t *testing.T) {
	store, post, _, _ := missHandlers(t)

	cases := map[string]string{
		"who asked":         `{"source":"bote","text":"Wann kommt der Müll?","day":"2026-10-16","user":"anna"}`,
		"a sender":          `{"source":"bote","text":"Wann kommt der Müll?","day":"2026-10-16","sender":"+49151"}`,
		"a count":           `{"source":"bote","text":"Wann kommt der Müll?","day":"2026-10-16","count":5}`,
		"no text":           `{"source":"bote","day":"2026-10-16"}`,
		"empty text":        missBody("bote", "", "2026-10-16"),
		"only spaces":       missBody("bote", "   ", "2026-10-16"),
		"too long":          missBody("bote", strings.Repeat("a", maxMissRunes+1), "2026-10-16"),
		"a newline":         missBody("bote", "Wann kommt\nder Müll?", "2026-10-16"),
		"a tab":             missBody("bote", "Wann\tkommt der Müll?", "2026-10-16"),
		"a DEL":             missBody("bote", "Wann kommt der Müll?\u007f", "2026-10-16"),
		"a C1 control":      missBody("bote", "Wann kommt der Müll?\u0085", "2026-10-16"),
		"invalid UTF-8":     `{"source":"bote","text":"Wann ` + "\xff" + `","day":"2026-10-16"}`,
		"no source":         `{"text":"Wann kommt der Müll?","day":"2026-10-16"}`,
		"upper-case source": missBody("Bote", "Wann kommt der Müll?", "2026-10-16"),
		"malformed day":     missBody("bote", "Wann kommt der Müll?", "2026-10-6"),
		"before the window": missBody("bote", "Wann kommt der Müll?", "2026-09-16"),
		"ahead of today":    missBody("bote", "Wann kommt der Müll?", "2026-10-17"),
		"not an object":     `"Wann kommt der Müll?"`,
		"two objects":       missBody("bote", "a", "2026-10-16") + missBody("bote", "b", "2026-10-16"),
		"empty body":        ``,
		"too large":         `{"source":"bote","text":"` + strings.Repeat("a", maxIngestBody) + `","day":"2026-10-16"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := postMiss(t, post, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
			}
			var e map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e["error"] == "" {
				t.Fatalf("want a JSON error, got %q", rec.Body.String())
			}
		})
	}
	if n := missRows(t, store); n != 0 {
		t.Fatalf("rejected requests stored %d rows", n)
	}
}

func TestMissRejectsWrongMethodAndContentType(t *testing.T) {
	_, post, list, _ := missHandlers(t)

	rec := httptest.NewRecorder()
	post.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/miss", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("GET /miss = %d Allow %q, want 405 POST", rec.Code, rec.Header().Get("Allow"))
	}

	req := httptest.NewRequest(http.MethodPost, "/miss", strings.NewReader(missBody("bote", "x", "2026-10-16")))
	req.Header.Set("Content-Type", "text/plain")
	rec = httptest.NewRecorder()
	post.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain = %d, want 415", rec.Code)
	}

	rec = httptest.NewRecorder()
	list.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/misses", nil))
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("POST /misses = %d Allow %q, want 405 GET", rec.Code, rec.Header().Get("Allow"))
	}
}

// A mis-pointed caller must not turn the store into a text dump: past 200
// different sentences per source and day a new one is refused and not stored,
// while one already there still counts up and other sources are untouched.
func TestMissCapsDistinctSentencesPerSourceAndDay(t *testing.T) {
	store, post, _, _ := missHandlers(t)

	for i := 0; i < maxMissesPerDay; i++ {
		if rec := postMiss(t, post, missBody("bote", fmt.Sprintf("Satz %d", i), "2026-10-16")); rec.Code != http.StatusOK {
			t.Fatalf("sentence %d = %d, body %s", i, rec.Code, rec.Body.String())
		}
	}

	rec := postMiss(t, post, missBody("bote", "Ein Satz zu viel", "2026-10-16"))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("sentence %d = %d, want 429; body %s", maxMissesPerDay+1, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("want a JSON error, got %s", rec.Body.String())
	}
	if n := missRows(t, store); n != maxMissesPerDay {
		t.Fatalf("stored %d rows, want %d", n, maxMissesPerDay)
	}

	rec = postMiss(t, post, missBody("bote", "Satz 7", "2026-10-16"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"count":2`) {
		t.Fatalf("a known sentence at the cap = %d %s, want 200 with count 2", rec.Code, rec.Body.String())
	}
	for _, body := range []string{missBody("stimme", "Ein Satz zu viel", "2026-10-16"), missBody("bote", "Ein Satz zu viel", "2026-10-15")} {
		if rec := postMiss(t, post, body); rec.Code != http.StatusOK {
			t.Fatalf("another source or day = %d, want 200; body %s", rec.Code, rec.Body.String())
		}
	}
}

// Retention is enforced by the service: with the clock moved 30 days on, the
// sweep deletes the sentence; one 29 days old survives. Counters keep their own,
// longer window.
func TestSweepForgetsMissesAfterThirtyDays(t *testing.T) {
	store, post, _, clock := missHandlers(t)
	r := newRetainer(store, defaultRetentionDays)
	r.now = clock.now

	postMiss(t, post, missBody("bote", "Wann kommt der Müll?", "2026-10-16"))
	seedCount(t, store, "haus", "page.start", "2026-10-16", 1)

	clock.advance(29 * 24 * time.Hour) // 2026-11-14: day 30 of the window
	if _, err := r.sweep(); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n := missRows(t, store); n != 1 {
		t.Fatalf("after 29 days %d rows, want the sentence to survive", n)
	}

	clock.advance(24 * time.Hour) // 2026-11-15: 30 days on
	if _, err := r.sweep(); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n := missRows(t, store); n != 0 {
		t.Fatalf("after 30 days %d rows, want the sentence gone", n)
	}
	if got := total(t, store, "haus", "page.start", "2026-10-16"); got != 1 {
		t.Fatalf("the counter went with the sentence (total %d); counters keep their own window", got)
	}
}

func TestSweepCountsDeletedMisses(t *testing.T) {
	store := newTestStore(t)
	clock := newClock(t, missNow)
	for _, day := range []string{"2026-09-15", "2026-09-16", "2026-09-17"} {
		if _, err := store.AddMiss("bote", "Satz", day, maxMissesPerDay); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	r := newRetainer(store, defaultRetentionDays)
	r.now = clock.now
	deleted, err := r.sweep()
	if err != nil || deleted != 2 {
		t.Fatalf("sweep = %d, %v; want the two misses before 2026-09-17", deleted, err)
	}
}

// Between two sweeps a sentence can be older than its window; the readout must
// not show it, whatever ?days= asks for.
func TestMissesNeverListsARowOutsideTheWindow(t *testing.T) {
	store, _, list, _ := missHandlers(t)
	for _, day := range []string{"2026-09-16", "2026-09-17", "2026-10-15", "2026-10-16"} {
		if _, err := store.AddMiss("bote", "am "+day, day, maxMissesPerDay); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	_, got := getMisses(t, list, "/misses?days=30")
	if got.Total != 3 || len(got.Misses) != 3 {
		t.Fatalf("days=30 = %+v, want the three rows from 2026-09-17 on", got)
	}
	for _, m := range got.Misses {
		if m.LastDay < "2026-09-17" {
			t.Fatalf("listed %+v, which is outside the window", m)
		}
	}

	_, got = getMisses(t, list, "/misses?days=1")
	if got.From != "2026-10-16" || got.Total != 1 || len(got.Misses) != 1 || got.Misses[0].Text != "am 2026-10-16" {
		t.Fatalf("days=1 = %+v, want only today", got)
	}
}

func TestMissesSortsByCountThenRecencyAndCapsTheList(t *testing.T) {
	store, _, list, _ := missHandlers(t)
	add := func(text, day string, n int) {
		for i := 0; i < n; i++ {
			if _, err := store.AddMiss("bote", text, day, maxMissesPerDay); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
	}
	add("selten", "2026-10-16", 1)
	add("oft", "2026-10-01", 5)
	add("oft", "2026-10-10", 4)
	add("mittel alt", "2026-10-02", 3)
	add("mittel neu", "2026-10-12", 3)
	add("b gleich", "2026-10-12", 3)

	_, got := getMisses(t, list, "/misses")
	order := []string{}
	for _, m := range got.Misses {
		order = append(order, m.Text)
	}
	if strings.Join(order, "|") != "oft|b gleich|mittel neu|mittel alt|selten" {
		t.Fatalf("order = %v", order)
	}
	if got.Misses[0].Count != 9 || got.Misses[0].LastDay != "2026-10-10" {
		t.Fatalf("oft = %+v, want 9 over two days, last 2026-10-10", got.Misses[0])
	}

	for i := 0; i < maxMissesListed+5; i++ {
		add(fmt.Sprintf("einmal %03d", i), "2026-10-14", 1)
	}
	_, got = getMisses(t, list, "/misses")
	if len(got.Misses) != maxMissesListed {
		t.Fatalf("listed %d, want at most %d", len(got.Misses), maxMissesListed)
	}
	if got.Total != 9+1+3+3+3+maxMissesListed+5 {
		t.Fatalf("total = %d, want every miss in the window, listed or not", got.Total)
	}
}

func TestMissesRendersAnEmptyArrayAndRejectsBadDays(t *testing.T) {
	_, _, list, _ := missHandlers(t)

	rec := httptest.NewRecorder()
	list.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/misses", nil))
	if !strings.Contains(rec.Body.String(), `"misses":[]`) {
		t.Fatalf("empty readout = %s, want \"misses\":[]", rec.Body.String())
	}

	for _, q := range []string{"0", "31", "-1", "abc", "1.5"} {
		rec, _ := getMisses(t, list, "/misses?days="+q)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("days=%s = %d, want 400", q, rec.Code)
		}
	}
}

func TestMissRoutesSurfaceAStoreFailure(t *testing.T) {
	store, post, list, _ := missHandlers(t)
	store.Close()

	if rec := postMiss(t, post, missBody("bote", "Satz", "2026-10-16")); rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST on a closed store = %d, want 500", rec.Code)
	}
	if rec, _ := getMisses(t, list, "/misses"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET on a closed store = %d, want 500", rec.Code)
	}
}

// Both routes read or write what the family said: the token gates them like the
// counters, and with it they answer through the real mux.
func TestMissRoutesNeedTheToken(t *testing.T) {
	mux := gatedMux(t)
	today := time.Now().UTC().Format(time.DateOnly)

	if rec := postMiss(t, mux, missBody("bote", "Satz", today)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /miss without token = %d, want 401", rec.Code)
	}
	if rec, _ := getMisses(t, mux, "/misses"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /misses without token = %d, want 401", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/miss", strings.NewReader(missBody("bote", "Satz", today)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /miss with token = %d, body %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/misses", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"text":"Satz"`) {
		t.Fatalf("GET /misses with token = %d %s", rec.Code, rec.Body.String())
	}
}
