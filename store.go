package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Store holds the aggregate counters. Nothing else is ever persisted: one row
// per (app, event, day), carrying a count and no other dimension.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS counters (
	app   TEXT    NOT NULL,
	event TEXT    NOT NULL,
	day   TEXT    NOT NULL,
	count INTEGER NOT NULL,
	PRIMARY KEY (app, event, day)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS misses (
	source TEXT    NOT NULL,
	text   TEXT    NOT NULL,
	day    TEXT    NOT NULL,
	count  INTEGER NOT NULL,
	PRIMARY KEY (source, text, day)
) WITHOUT ROWID;
`

// OpenStore opens (creating if needed) the counter database at path.
func OpenStore(path string) (*Store, error) {
	// WAL keeps concurrent ingest calls from tripping "database is locked", and
	// busy_timeout makes the writer queue rather than fail when they collide.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)",
		url.PathEscape(filepath.ToSlash(path)),
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Increment adds count to the (app, event, day) counter and returns the new
// total. The upsert is a single statement, so concurrent callers add up instead
// of overwriting each other.
func (s *Store) Increment(app, event, day string, count int64) (int64, error) {
	var total int64
	err := s.db.QueryRow(`
		INSERT INTO counters (app, event, day, count) VALUES (?, ?, ?, ?)
		ON CONFLICT (app, event, day) DO UPDATE SET count = count + excluded.count
		RETURNING count`,
		app, event, day, count,
	).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total, nil
}

// DeleteBefore removes every counter dated before counterDay and every miss
// dated before missDay (both exclusive) and returns how many rows were deleted.
// Days are stored as YYYY-MM-DD, so the string comparison is a chronological one.
func (s *Store) DeleteBefore(counterDay, missDay string) (int64, error) {
	var deleted int64
	for _, del := range []struct{ query, day string }{
		{"DELETE FROM counters WHERE day < ?", counterDay},
		{"DELETE FROM misses WHERE day < ?", missDay},
	} {
		result, err := s.db.Exec(del.query, del.day)
		if err != nil {
			return deleted, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return deleted, err
		}
		deleted += n
	}
	return deleted, nil
}

func (s *Store) journalMode() (string, error) {
	var mode string
	err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	return mode, err
}

// counterTotal is one (app, event) pair summed over a range of days.
type counterTotal struct {
	App   string
	Event string
	Count int64
}

// TotalsSince sums the counters for every day on or after day, one row per
// (app, event). Days are stored as YYYY-MM-DD, so the string comparison is a
// chronological one.
func (s *Store) TotalsSince(day string) ([]counterTotal, error) {
	rows, err := s.db.Query(`
		SELECT app, event, SUM(count) FROM counters
		WHERE day >= ?
		GROUP BY app, event
		ORDER BY app, event`, day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	totals := []counterTotal{}
	for rows.Next() {
		var t counterTotal
		if err := rows.Scan(&t.App, &t.Event, &t.Count); err != nil {
			return nil, err
		}
		totals = append(totals, t)
	}
	return totals, rows.Err()
}

// Apps lists every app the database still holds a counter for, whatever its
// day. It is what separates "nothing has ever reported" from "this app has
// reported before, just not inside the window being asked about".
func (s *Store) Apps() ([]string, error) {
	rows, err := s.db.Query("SELECT DISTINCT app FROM counters ORDER BY app")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	apps := []string{}
	for rows.Next() {
		var app string
		if err := rows.Scan(&app); err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

// errMissCap is AddMiss refusing a new sentence for a (source, day) that
// already holds the cap's worth of different ones.
var errMissCap = errors.New("too many different sentences for this source and day")

// AddMiss counts one more occurrence of the sentence for (source, day) and
// returns its count for that day. A sentence not stored yet is added only while
// the (source, day) holds fewer than maxPerDay different ones; the check and
// the write are one statement, so concurrent callers cannot overshoot the cap.
func (s *Store) AddMiss(source, text, day string, maxPerDay int) (int64, error) {
	var count int64
	// The WHERE makes SQLite read ON CONFLICT as the upsert, not a join.
	err := s.db.QueryRow(`
		INSERT INTO misses (source, text, day, count)
		SELECT ?, ?, ?, 1
		WHERE EXISTS (SELECT 1 FROM misses WHERE source = ? AND text = ? AND day = ?)
		   OR (SELECT COUNT(*) FROM misses WHERE source = ? AND day = ?) < ?
		ON CONFLICT (source, text, day) DO UPDATE SET count = count + 1
		RETURNING count`,
		source, text, day, source, text, day, source, day, maxPerDay,
	).Scan(&count)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errMissCap
	}
	return count, err
}

// MissesSince sums each (source, text) over the days on or after day, most
// frequent first, at most limit of them, and the total over all of them.
func (s *Store) MissesSince(day string, limit int) ([]missTotal, int64, error) {
	var total int64
	if err := s.db.QueryRow("SELECT COALESCE(SUM(count), 0) FROM misses WHERE day >= ?", day).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(`
		SELECT source, text, SUM(count) AS n, MAX(day) AS last FROM misses
		WHERE day >= ?
		GROUP BY source, text
		ORDER BY n DESC, last DESC, text, source
		LIMIT ?`, day, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	misses := []missTotal{}
	for rows.Next() {
		var m missTotal
		if err := rows.Scan(&m.Source, &m.Text, &m.Count, &m.LastDay); err != nil {
			return nil, 0, err
		}
		misses = append(misses, m)
	}
	return misses, total, rows.Err()
}
