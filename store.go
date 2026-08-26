package main

import (
	"database/sql"
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

// DeleteBefore removes every counter dated before day (exclusive) and returns
// how many rows were deleted. Days are stored as YYYY-MM-DD, so the string
// comparison is a chronological one.
func (s *Store) DeleteBefore(day string) (int64, error) {
	result, err := s.db.Exec("DELETE FROM counters WHERE day < ?", day)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
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
