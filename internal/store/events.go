package store

import (
	"fmt"
	"time"
)

// EventRetention is how long the notification log is kept.
//
// A year, because the questions it answers are seasonal: whether the filter
// alarm came at the same time last autumn, how often the bus went quiet over a
// winter. The rows are a few hundred bytes and there are a handful a month, so
// the cost of keeping them is nothing next to the measurements beside them.
const EventRetention = 365 * 24 * time.Hour

// Event is a notification as it was recorded.
//
// Delivered is the point of writing these down at all. A condition that was
// raised while no channel was configured, or one below the severity somebody
// asked to hear about, leaves no trace anywhere else — and "why was I not told"
// is exactly the question this table exists to answer.
type Event struct {
	Time      time.Time `json:"time"`
	Key       string    `json:"key"`
	Kind      string    `json:"kind"`
	Severity  string    `json:"severity"`
	Title     string    `json:"title"`
	Message   string    `json:"message"`
	Resolved  bool      `json:"resolved"`
	Delivered bool      `json:"delivered"`
	// Detail says where it went, or why it did not.
	Detail string `json:"detail,omitempty"`
}

// AppendEvent writes one down.
func (s *Store) AppendEvent(e Event) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	_, err := s.db.Exec(
		`INSERT INTO events (ts, "key", kind, severity, title, message, resolved, delivered, detail)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Time.Unix(), e.Key, e.Kind, e.Severity, e.Title, e.Message,
		e.Resolved, e.Delivered, e.Detail)
	if err != nil {
		return fmt.Errorf("store: append event: %w", err)
	}
	return nil
}

// Events returns what was recorded since a moment, newest first.
//
// Newest first because that is the order somebody reads them in: the question
// is almost always what just happened, and the history below it is context.
func (s *Store) Events(since time.Time, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		`SELECT ts, "key", kind, severity, title, message, resolved, delivered, detail
		   FROM events WHERE ts >= ? ORDER BY ts DESC, id DESC LIMIT ?`,
		since.Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("store: read events: %w", err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		var ts int64
		if err := rows.Scan(&ts, &e.Key, &e.Kind, &e.Severity, &e.Title,
			&e.Message, &e.Resolved, &e.Delivered, &e.Detail); err != nil {
			return nil, fmt.Errorf("store: read events: %w", err)
		}
		e.Time = time.Unix(ts, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountEvents is how many were recorded since a moment.
//
// Cheap enough to run beside the rest of the system page: it is a count over an
// index. It exists so the collapsed history can say how much is behind it —
// "Show history" with nothing to show is a door into an empty room.
func (s *Store) CountEvents(since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM events WHERE ts >= ?`, since.Unix()).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count events: %w", err)
	}
	return n, nil
}
