package main

import "time"

// windowStart is the oldest day a window of `days` covers: the last `days`
// calendar days in UTC, counting today as the first. A row dated exactly on it
// is inside the window; the day before it is outside.
//
// Retention and the summary readout share this one definition on purpose. Two
// copies would drift at the boundary, and then the readout would either show a
// day retention had already deleted or hide one it kept.
func windowStart(now time.Time, days int) string {
	return now.UTC().AddDate(0, 0, -(days - 1)).Format(time.DateOnly)
}
