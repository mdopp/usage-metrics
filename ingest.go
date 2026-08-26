package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// maxIngestBody is generous for the four-field increment this endpoint accepts
// and small enough that a mis-pointed caller cannot stream a payload at us.
const maxIngestBody = 4 << 10

// Lower-case only: accepting case variants would silently fragment the counter
// for one event across several rows.
var (
	appPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	eventPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

type increment struct {
	App   string `json:"app"`
	Event string `json:"event"`
	Day   string `json:"day"`
	Count int64  `json:"count"`
}

type ingestResponse struct {
	App   string `json:"app"`
	Event string `json:"event"`
	Day   string `json:"day"`
	Total int64  `json:"total"`
}

type ingestHandler struct {
	store *Store
}

func (h *ingestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "only POST is accepted")
		return
	}
	if !hasJSONContentType(r) {
		writeError(w, http.StatusUnsupportedMediaType, "content-type must be application/json")
		return
	}

	inc, err := decodeIncrement(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	total, err := h.store.Increment(inc.App, inc.Event, inc.Day, inc.Count)
	if err != nil {
		log.Printf("ingest: store %s/%s/%s: %v", inc.App, inc.Event, inc.Day, err)
		writeError(w, http.StatusInternalServerError, "could not record the increment")
		return
	}

	writeJSON(w, http.StatusOK, ingestResponse{App: inc.App, Event: inc.Event, Day: inc.Day, Total: total})
}

func decodeIncrement(w http.ResponseWriter, r *http.Request) (increment, error) {
	var inc increment
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxIngestBody))
	// Unknown fields are rejected on purpose: this endpoint stores counters, and
	// a caller sending anything else (content, a user id) must find that out.
	dec.DisallowUnknownFields()

	if err := dec.Decode(&inc); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.Is(err, io.EOF):
			return inc, errors.New("body must be a JSON object")
		case errors.As(err, &maxErr):
			return inc, errors.New("body is too large")
		default:
			return inc, errors.New("body is not a valid increment: " + err.Error())
		}
	}
	if dec.More() {
		return inc, errors.New("body must contain exactly one JSON object")
	}

	switch {
	case !appPattern.MatchString(inc.App):
		return inc, errors.New(`"app" must match [a-z0-9][a-z0-9._-]{0,63}`)
	case !eventPattern.MatchString(inc.Event):
		return inc, errors.New(`"event" must match [a-z0-9][a-z0-9._-]{0,127}`)
	case !isCalendarDay(inc.Day):
		return inc, errors.New(`"day" must be a calendar date formatted YYYY-MM-DD`)
	case inc.Count < 1:
		return inc, errors.New(`"count" must be a positive integer`)
	}
	return inc, nil
}

func isCalendarDay(day string) bool {
	parsed, err := time.Parse(time.DateOnly, day)
	// time.Parse accepts "2026-7-3"; comparing the round-trip rejects anything
	// that is not exactly YYYY-MM-DD.
	return err == nil && parsed.Format(time.DateOnly) == day
}

func hasJSONContentType(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("ingest: write response: %v", err)
	}
}
