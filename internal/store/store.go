// Package store keeps the measurement history.
//
// The unit refreshes its measurements every ten seconds and we read all of
// them, so the raw material is plentiful and the interesting question is what
// to keep. Four tiers answer it: fine detail for the last two days, when
// somebody is looking at why the house felt cold last night, and an hour at a
// time forever, when the question is how this winter compares with the last.
//
// The whole history is one file. That matters more than it sounds: a backup is
// a copy, and restoring it onto a fresh card is a copy back.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

// Tier is one resolution of stored history.
type Tier struct {
	Level  int
	Bucket time.Duration
	// Retain is how long data at this resolution is kept. Zero means forever.
	Retain time.Duration
	Name   string
}

// Tiers, finest first. Tier 0 is what the poller writes; the rest are rolled
// up from the one above.
var Tiers = []Tier{
	{Level: 0, Bucket: 10 * time.Second, Retain: 48 * time.Hour, Name: "10 s"},
	{Level: 1, Bucket: time.Minute, Retain: 30 * 24 * time.Hour, Name: "1 min"},
	{Level: 2, Bucket: 15 * time.Minute, Retain: 365 * 24 * time.Hour, Name: "15 min"},
	{Level: 3, Bucket: time.Hour, Retain: 0, Name: "1 time"},
}

// Store is the history database.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS samples (
  tier   INTEGER NOT NULL,
  metric INTEGER NOT NULL,
  ts     INTEGER NOT NULL,
  avg    REAL NOT NULL,
  min    REAL NOT NULL,
  max    REAL NOT NULL,
  PRIMARY KEY (tier, metric, ts)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS metrics (
  id   INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS events (
  id        INTEGER PRIMARY KEY,
  ts        INTEGER NOT NULL,
  "key"     TEXT NOT NULL,
  kind      TEXT NOT NULL,
  severity  TEXT NOT NULL,
  title     TEXT NOT NULL,
  message   TEXT NOT NULL,
  resolved  INTEGER NOT NULL,
  delivered INTEGER NOT NULL,
  detail    TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS events_ts ON events (ts);
`

// Open opens or creates the database at path.
func Open(path string) (*Store, error) {
	// WAL so a reader never blocks the writer, and NORMAL so the writer is not
	// calling fsync on every commit. On an SD card that difference is the
	// difference between a card that lasts and one that does not.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// One writer. SQLite allows only one anyway, and letting the pool open
	// several just turns contention into SQLITE_BUSY.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: create schema: %w", err)
	}
	s := &Store{db: db}
	if err := s.registerMetrics(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// registerMetrics writes the metric names, so the file explains itself to
// anyone who opens it with a SQLite browser rather than through this code.
func (s *Store) registerMetrics() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO metrics (id, name) VALUES (?, ?)
	                         ON CONFLICT(id) DO UPDATE SET name = excluded.name`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, m := range Metrics {
		if _, err := stmt.Exec(int(m.ID), m.Name); err != nil {
			return fmt.Errorf("store: register metric %s: %w", m.Name, err)
		}
	}
	return tx.Commit()
}

// Sample is one metric's value at one moment.
type Sample struct {
	Metric Metric
	Value  float64
}

// Record stores a set of readings taken at ts, in the finest tier.
//
// Readings land in the bucket they fall in, and a second reading in the same
// bucket replaces the first. At ten second buckets and a ten second poll that
// is rare, but a re-read after a write produces one and the newer value is the
// one worth keeping.
func (s *Store) Record(ts time.Time, samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	bucket := truncate(ts, Tiers[0].Bucket)

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO samples (tier, metric, ts, avg, min, max)
	                         VALUES (0, ?, ?, ?, ?, ?)
	                         ON CONFLICT(tier, metric, ts)
	                         DO UPDATE SET avg = excluded.avg, min = excluded.min, max = excluded.max`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, sample := range samples {
		if _, err := stmt.Exec(int(sample.Metric), bucket, sample.Value, sample.Value, sample.Value); err != nil {
			return fmt.Errorf("store: record %d: %w", sample.Metric, err)
		}
	}
	return tx.Commit()
}

// Rollup aggregates completed buckets from each tier into the next.
//
// Only buckets that have certainly finished are rolled up, which is why the
// cutoff is one bucket back from now: aggregating the bucket in progress would
// freeze a partial average into the coarser tier for good.
func (s *Store) Rollup(now time.Time) error {
	for i := 1; i < len(Tiers); i++ {
		if err := s.rollupInto(Tiers[i-1], Tiers[i], now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) rollupInto(from, to Tier, now time.Time) error {
	var start int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(ts), 0) FROM samples WHERE tier = ?`, to.Level).Scan(&start)
	if err != nil {
		return fmt.Errorf("store: newest bucket in tier %d: %w", to.Level, err)
	}
	// Redo the newest coarse bucket: more fine buckets may have arrived for it
	// since it was last written, and leaving it half aggregated would be worse
	// than recomputing it.
	cutoff := truncate(now, to.Bucket)
	if start >= cutoff {
		return nil
	}

	secs := int64(to.Bucket / time.Second)
	_, err = s.db.Exec(`
		INSERT INTO samples (tier, metric, ts, avg, min, max)
		SELECT ?, metric, (ts / ?) * ?, AVG(avg), MIN(min), MAX(max)
		FROM samples
		WHERE tier = ? AND ts >= ? AND ts < ?
		GROUP BY metric, ts / ?
		ON CONFLICT(tier, metric, ts)
		DO UPDATE SET avg = excluded.avg, min = excluded.min, max = excluded.max`,
		to.Level, secs, secs, from.Level, start, cutoff, secs)
	if err != nil {
		return fmt.Errorf("store: roll tier %d into %d: %w", from.Level, to.Level, err)
	}
	return nil
}

// Prune deletes data past each tier's retention.
func (s *Store) Prune(now time.Time) error {
	for _, t := range Tiers {
		if t.Retain == 0 {
			continue
		}
		cutoff := now.Add(-t.Retain).Unix()
		if _, err := s.db.Exec(`DELETE FROM samples WHERE tier = ? AND ts < ?`, t.Level, cutoff); err != nil {
			return fmt.Errorf("store: prune tier %d: %w", t.Level, err)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM events WHERE ts < ?`,
		now.Add(-EventRetention).Unix()); err != nil {
		return fmt.Errorf("store: prune events: %w", err)
	}
	return nil
}

// Point is one aggregated value.
type Point struct {
	Time time.Time `json:"t"`
	Avg  float64   `json:"v"`
	Min  float64   `json:"lo"`
	Max  float64   `json:"hi"`
}

// Series is one metric over a range of time.
type Series struct {
	Metric     Metric `json:"metric"`
	Name       string `json:"name"`
	Label      string `json:"label"`
	Unit       string `json:"unit"`
	Resolution string `json:"resolution"`
	// Binary marks a series that is only ever 0 or 1 at full resolution, which
	// a chart draws as a band rather than as a line wandering between two
	// values. Rolled up it is the share of the bucket the condition held for.
	Binary bool    `json:"binary"`
	Points []Point `json:"points"`
}

// Query returns a metric's history between from and to.
//
// The tier is chosen by what the window needs rather than by what the caller
// asks for: the finest one that both reaches back far enough and does not
// return more points than a chart can draw. Asking for a year at ten second
// resolution would be three million points nobody can see.
func (s *Store) Query(metric Metric, from, to time.Time, maxPoints int) (*Series, error) {
	if maxPoints <= 0 {
		// Generous: uPlot draws tens of thousands of points without effort,
		// and coarser resolution than the window needs throws away detail the
		// database went to the trouble of keeping.
		maxPoints = 2000
	}
	def, ok := MetricByID(metric)
	if !ok {
		return nil, fmt.Errorf("store: no metric %d", metric)
	}
	// The ideal tier may be empty: the coarser ones are filled by roll-up, so
	// shortly after a fresh start only the finest has anything in it. Falling
	// back towards finer data costs more points but shows the truth, which
	// beats an empty chart on a system that is plainly recording.
	chosen := chooseTier(from, to, maxPoints, time.Now())
	for level := chosen.Level; level >= 0; level-- {
		t := Tiers[level]
		series, err := s.read(metric, def, t, from, to)
		if err != nil {
			return nil, err
		}
		if len(series.Points) > 0 || level == 0 {
			return series, nil
		}
	}
	return s.read(metric, def, chosen, from, to)
}

func (s *Store) read(metric Metric, def Definition, t Tier, from, to time.Time) (*Series, error) {
	rows, err := s.db.Query(`
		SELECT ts, avg, min, max FROM samples
		WHERE tier = ? AND metric = ? AND ts >= ? AND ts <= ?
		ORDER BY ts`, t.Level, int(metric), from.Unix(), to.Unix())
	if err != nil {
		return nil, fmt.Errorf("store: query %s: %w", def.Name, err)
	}
	defer rows.Close()

	series := &Series{
		Metric: metric, Name: def.Name, Label: def.Label,
		Unit: def.Unit, Resolution: t.Name, Binary: def.Binary,
	}
	for rows.Next() {
		var ts int64
		var p Point
		if err := rows.Scan(&ts, &p.Avg, &p.Min, &p.Max); err != nil {
			return nil, err
		}
		p.Time = time.Unix(ts, 0)
		series.Points = append(series.Points, p)
	}
	return series, rows.Err()
}

// chooseTier picks the finest resolution that covers the window without
// producing more points than asked for.
func chooseTier(from, to time.Time, maxPoints int, now time.Time) Tier {
	span := to.Sub(from)
	for _, t := range Tiers {
		// A tier whose retention does not reach back to the start of the
		// window would draw a line that simply begins late.
		if t.Retain > 0 && now.Sub(from) > t.Retain {
			continue
		}
		if int(span/t.Bucket) <= maxPoints {
			return t
		}
	}
	return Tiers[len(Tiers)-1]
}

// Stats describes what the database holds, for the system page.
type Stats struct {
	Rows      map[int]int64 `json:"rows"`
	Oldest    time.Time     `json:"oldest"`
	Newest    time.Time     `json:"newest"`
	SizeBytes int64         `json:"size_bytes"`
}

// Snapshot writes a consistent copy of the database to path.
//
// Through SQLite rather than copying the file: a database copied while it is
// being written to is one that may not open, and the whole point of a backup is
// the day somebody needs it.
func (s *Store) Snapshot(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("store: clear %s: %w", path, err)
	}
	// VACUUM INTO takes its own read transaction, so the copy is a single
	// point in time even while the recorder is writing.
	if _, err := s.db.Exec("VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("store: copy to %s: %w", path, err)
	}
	return nil
}

// Stats summarises the database.
func (s *Store) Stats() (*Stats, error) {
	out := &Stats{Rows: map[int]int64{}}
	rows, err := s.db.Query(`SELECT tier, COUNT(*), MIN(ts), MAX(ts) FROM samples GROUP BY tier`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var tier int
		var count, oldest, newest int64
		if err := rows.Scan(&tier, &count, &oldest, &newest); err != nil {
			return nil, err
		}
		out.Rows[tier] = count
		if t := time.Unix(oldest, 0); out.Oldest.IsZero() || t.Before(out.Oldest) {
			out.Oldest = t
		}
		if t := time.Unix(newest, 0); t.After(out.Newest) {
			out.Newest = t
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var pageCount, pageSize int64
	if err := s.db.QueryRow(`SELECT page_count * page_size FROM pragma_page_count(), pragma_page_size()`).Scan(&pageCount); err == nil {
		out.SizeBytes = pageCount
	}
	_ = pageSize
	return out, nil
}

// truncate rounds a time down to the start of its bucket, as a unix second.
func truncate(t time.Time, bucket time.Duration) int64 {
	secs := int64(bucket / time.Second)
	if secs <= 0 {
		return t.Unix()
	}
	return (t.Unix() / secs) * secs
}
