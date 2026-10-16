package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Unanswered sentences (owner decision 2026-10-16): the one place this service
// keeps free text. Only the wording, the source and the day — never who asked —
// and only for missRetentionDays, whatever the counters' window is.
const (
	missRetentionDays = 30
	maxMissRunes      = 280
	maxMissesPerDay   = 200
	maxMissesListed   = 100
)

type missRequest struct {
	Source string `json:"source"`
	Text   string `json:"text"`
	Day    string `json:"day"`
}

type missResponse struct {
	Source string `json:"source"`
	Day    string `json:"day"`
	Count  int64  `json:"count"`
}

type missHandler struct {
	store *Store
	now   func() time.Time
}

func (h *missHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "only POST is accepted")
		return
	}
	if !hasJSONContentType(r) {
		writeError(w, http.StatusUnsupportedMediaType, "content-type must be application/json")
		return
	}

	miss, err := decodeMiss(w, r, h.now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	count, err := h.store.AddMiss(miss.Source, miss.Text, miss.Day, maxMissesPerDay)
	switch {
	case errors.Is(err, errMissCap):
		writeError(w, http.StatusTooManyRequests, "this source already has "+strconv.Itoa(maxMissesPerDay)+" different sentences for this day")
		return
	case err != nil:
		log.Printf("miss: store %s/%s: %v", miss.Source, miss.Day, err)
		writeError(w, http.StatusInternalServerError, "could not record the sentence")
		return
	}
	writeJSON(w, http.StatusOK, missResponse{Source: miss.Source, Day: miss.Day, Count: count})
}

func decodeMiss(w http.ResponseWriter, r *http.Request, now time.Time) (missRequest, error) {
	var m missRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxIngestBody))
	// Unknown fields are refused on purpose: a caller that sends who asked must
	// find out here, not have it silently dropped.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.Is(err, io.EOF):
			return m, errors.New("body must be a JSON object")
		case errors.As(err, &maxErr):
			return m, errors.New("body is too large")
		default:
			return m, errors.New("body is not a valid miss: " + err.Error())
		}
	}
	if dec.More() {
		return m, errors.New("body must contain exactly one JSON object")
	}

	m.Text = strings.TrimSpace(m.Text)
	today := now.UTC().Format(time.DateOnly)
	switch {
	case !appPattern.MatchString(m.Source):
		return m, errors.New(`"source" must match [a-z0-9][a-z0-9._-]{0,63}`)
	case m.Text == "":
		return m, errors.New(`"text" must not be empty`)
	// encoding/json turns invalid UTF-8 into U+FFFD, so that is what a broken
	// encoding looks like by now; no sentence a person said contains it.
	case strings.ContainsRune(m.Text, utf8.RuneError):
		return m, errors.New(`"text" must be valid UTF-8`)
	case utf8.RuneCountInString(m.Text) > maxMissRunes:
		return m, errors.New(`"text" must be at most ` + strconv.Itoa(maxMissRunes) + ` characters`)
	case strings.IndexFunc(m.Text, unicode.IsControl) >= 0:
		return m, errors.New(`"text" must be one line without control characters`)
	case !isCalendarDay(m.Day):
		return m, errors.New(`"day" must be a calendar date formatted YYYY-MM-DD`)
	case m.Day < windowStart(now, missRetentionDays) || m.Day > today:
		return m, errors.New(`"day" must lie within the last ` + strconv.Itoa(missRetentionDays) + ` days, today included`)
	}
	return m, nil
}

// missTotal is one sentence of one source summed over the window.
type missTotal struct {
	Source  string `json:"source"`
	Text    string `json:"text"`
	Count   int64  `json:"count"`
	LastDay string `json:"lastDay"`
}

// missList is the readout. Total counts every miss in the window, also those
// past the listed maxMissesListed.
type missList struct {
	Days   int         `json:"days"`
	From   string      `json:"from"`
	Total  int64       `json:"total"`
	Misses []missTotal `json:"misses"`
}

type missesHandler struct {
	store *Store
	now   func() time.Time
}

func (h *missesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "only GET is accepted")
		return
	}
	days := missRetentionDays
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > missRetentionDays {
			writeError(w, http.StatusBadRequest, `"days" must be an integer between 1 and `+strconv.Itoa(missRetentionDays))
			return
		}
		days = n
	}

	from := windowStart(h.now(), days)
	misses, total, err := h.store.MissesSince(from, maxMissesListed)
	if err != nil {
		log.Printf("misses: read: %v", err)
		writeError(w, http.StatusInternalServerError, "could not read the sentences")
		return
	}
	writeJSON(w, http.StatusOK, missList{Days: days, From: from, Total: total, Misses: misses})
}
