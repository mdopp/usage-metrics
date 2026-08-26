package main

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"
)

// maxSummaryDays is far past any retention window an operator would set; it
// exists so a fat-fingered ?days= cannot walk the calendar off its hinges.
const maxSummaryDays = 3650

type eventTotal struct {
	Event string `json:"event"`
	Count int64  `json:"count"`
}

type appTotals struct {
	App    string       `json:"app"`
	Total  int64        `json:"total"`
	Events []eventTotal `json:"events"`
}

// summary is the readout. `apps` holds only what was counted inside the window,
// so an app with nothing this window is absent from it — `knownApps` is what
// tells a reader whether that app has ever reported at all, which is why an
// empty `apps` is never ambiguous.
type summary struct {
	Days      int         `json:"days"`
	From      string      `json:"from"`
	Total     int64       `json:"total"`
	Apps      []appTotals `json:"apps"`
	KnownApps []string    `json:"knownApps"`
}

// summarize aggregates the counters inside the window into per-app × event
// totals. The window is the shared one (see windowStart), so the readout covers
// exactly the days retention keeps for the same number of days.
func summarize(store *Store, now time.Time, days int) (summary, error) {
	from := windowStart(now, days)

	totals, err := store.TotalsSince(from)
	if err != nil {
		return summary{}, err
	}
	knownApps, err := store.Apps()
	if err != nil {
		return summary{}, err
	}

	// TotalsSince orders by (app, event), so each app's rows arrive together.
	result := summary{Days: days, From: from, Apps: []appTotals{}, KnownApps: knownApps}
	for _, t := range totals {
		if len(result.Apps) == 0 || result.Apps[len(result.Apps)-1].App != t.App {
			result.Apps = append(result.Apps, appTotals{App: t.App, Events: []eventTotal{}})
		}
		app := &result.Apps[len(result.Apps)-1]
		app.Events = append(app.Events, eventTotal{Event: t.Event, Count: t.Count})
		app.Total += t.Count
		result.Total += t.Count
	}
	return result, nil
}

type summaryHandler struct {
	store *Store
	// defaultDays is the retention window: asked nothing, the readout shows
	// everything the box still holds rather than an arbitrary slice of it.
	defaultDays int
	now         func() time.Time
}

func (h *summaryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "only GET is accepted")
		return
	}

	days, err := summaryDays(r.URL.Query().Get("days"), h.defaultDays)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := summarize(h.store, h.now(), days)
	if err != nil {
		log.Printf("summary: read counters: %v", err)
		writeError(w, http.StatusInternalServerError, "could not read the counters")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func summaryDays(raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 1 || days > maxSummaryDays {
		return 0, errors.New(`"days" must be an integer between 1 and ` + strconv.Itoa(maxSummaryDays))
	}
	return days, nil
}
