package store

import (
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRecordAndQuery(t *testing.T) {
	s := open(t)
	base := time.Now().Add(-time.Hour).Truncate(time.Minute)

	for i := 0; i < 6; i++ {
		ts := base.Add(time.Duration(i) * 10 * time.Second)
		if err := s.Record(ts, []Sample{{MetricTempFresh, float64(i)}}); err != nil {
			t.Fatal(err)
		}
	}

	series, err := s.Query(MetricTempFresh, base.Add(-time.Minute), base.Add(time.Minute), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Points) != 6 {
		t.Fatalf("%d points, want 6", len(series.Points))
	}
	if series.Points[0].Avg != 0 || series.Points[5].Avg != 5 {
		t.Fatalf("values = %v .. %v, want 0 .. 5", series.Points[0].Avg, series.Points[5].Avg)
	}
	if series.Label == "" || series.Unit != "°C" {
		t.Errorf("series is missing its labelling: %+v", series)
	}
}

func TestRecordReplacesWithinTheSameBucket(t *testing.T) {
	// A re-read straight after a write lands in the same ten second bucket as
	// the poll that preceded it, and the newer value is the one worth keeping.
	s := open(t)
	// Anchored to a bucket boundary: two seconds later is only the same bucket
	// if the first reading did not land near the end of one.
	ts := time.Unix(truncate(time.Now().Add(-time.Hour), Tiers[0].Bucket), 0)

	if err := s.Record(ts, []Sample{{MetricSetpoint, 21}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(ts.Add(2*time.Second), []Sample{{MetricSetpoint, 22}}); err != nil {
		t.Fatal(err)
	}

	series, err := s.Query(MetricSetpoint, ts.Add(-time.Minute), ts.Add(time.Minute), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Points) != 1 {
		t.Fatalf("%d points, want 1", len(series.Points))
	}
	if series.Points[0].Avg != 22 {
		t.Fatalf("value = %v, want the newer 22", series.Points[0].Avg)
	}
}

func TestRollupAggregatesCompletedBucketsOnly(t *testing.T) {
	// Rolling up the bucket in progress would freeze a partial average into
	// the coarser tier for good.
	s := open(t)
	now := time.Now().Truncate(time.Minute)
	past := now.Add(-2 * time.Minute)

	// A full minute of readings in the completed bucket, and one in the
	// current minute that must not be rolled up yet.
	for i := 0; i < 6; i++ {
		if err := s.Record(past.Add(time.Duration(i)*10*time.Second), []Sample{{MetricTempFresh, float64(10 + i)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Record(now.Add(5*time.Second), []Sample{{MetricTempFresh, 99}}); err != nil {
		t.Fatal(err)
	}

	if err := s.Rollup(now.Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}

	var count int
	var avg, lo, hi float64
	err := s.db.QueryRow(`SELECT COUNT(*), AVG(avg), MIN(min), MAX(max) FROM samples WHERE tier = 1 AND metric = ?`,
		int(MetricTempFresh)).Scan(&count, &avg, &lo, &hi)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("%d minute buckets, want 1; the minute in progress was rolled up", count)
	}
	if avg != 12.5 {
		t.Errorf("average = %v, want 12.5 (10 through 15)", avg)
	}
	if lo != 10 || hi != 15 {
		t.Errorf("range = %v..%v, want 10..15", lo, hi)
	}
}

func TestRollupKeepsTheExtremesThroughEveryTier(t *testing.T) {
	// The point of keeping min and max is that a brief cold snap survives
	// being averaged into an hour.
	s := open(t)
	now := time.Now().Truncate(time.Hour)
	start := now.Add(-2 * time.Hour)

	for i := 0; i < 360; i++ { // an hour of ten second readings
		v := 20.0
		if i == 100 {
			v = -8 // one brief excursion
		}
		if err := s.Record(start.Add(time.Duration(i)*10*time.Second), []Sample{{MetricTempFresh, v}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Rollup(now); err != nil {
		t.Fatal(err)
	}

	var lo float64
	if err := s.db.QueryRow(`SELECT MIN(min) FROM samples WHERE tier = 3 AND metric = ?`,
		int(MetricTempFresh)).Scan(&lo); err != nil {
		t.Fatal(err)
	}
	if lo != -8 {
		t.Fatalf("minimum at hourly resolution = %v, want -8; the excursion was averaged away", lo)
	}
}

func TestRollupIsIdempotent(t *testing.T) {
	s := open(t)
	now := time.Now().Truncate(time.Minute)
	for i := 0; i < 6; i++ {
		if err := s.Record(now.Add(-2*time.Minute+time.Duration(i)*10*time.Second), []Sample{{MetricTempFresh, 20}}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := s.Rollup(now); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM samples WHERE tier = 1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("%d rows after rolling up three times, want 1", count)
	}
}

func TestPruneRemovesOnlyWhatIsPastRetention(t *testing.T) {
	s := open(t)
	now := time.Now()
	old := now.Add(-72 * time.Hour) // past tier 0's 48 hours
	recent := now.Add(-time.Hour)

	if err := s.Record(old, []Sample{{MetricTempFresh, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(recent, []Sample{{MetricTempFresh, 2}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(now); err != nil {
		t.Fatal(err)
	}

	series, err := s.Query(MetricTempFresh, now.Add(-47*time.Hour), now, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Points) != 1 || series.Points[0].Avg != 2 {
		t.Fatalf("points = %+v, want only the recent one", series.Points)
	}
	// And the pruned reading really is gone, not merely outside the window.
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM samples WHERE tier = 0`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("%d rows left in tier 0, want 1", count)
	}
}

func TestPruneNeverTouchesTheTierKeptForever(t *testing.T) {
	s := open(t)
	now := time.Now()
	ancient := now.Add(-10 * 365 * 24 * time.Hour)

	if _, err := s.db.Exec(`INSERT INTO samples VALUES (3, ?, ?, 5, 5, 5)`,
		int(MetricTempFresh), ancient.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(now); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM samples WHERE tier = 3`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("ten year old hourly data was pruned; that tier is kept for good")
	}
}

func TestChooseTierByWhatTheWindowNeeds(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		span  time.Duration
		want  int
		limit int
	}{
		{"an hour wants full detail", time.Hour, 0, 2000},
		// A day at ten seconds is 8640 points; at a minute it is 1440, which
		// is still more than a phone has pixels but well within what uPlot
		// draws without effort.
		{"a day steps down to minutes", 24 * time.Hour, 1, 2000},
		{"a week needs coarser still", 7 * 24 * time.Hour, 2, 2000},
		{"a year is hourly", 365 * 24 * time.Hour, 3, 2000},
	}
	for _, c := range cases {
		got := chooseTier(now.Add(-c.span), now, c.limit, now)
		if got.Level != c.want {
			t.Errorf("%s: tier %d, want %d", c.name, got.Level, c.want)
		}
	}
}

func TestQueryFallsBackToFinerDataRatherThanShowingNothing(t *testing.T) {
	// Coarse tiers are filled by roll-up, so shortly after a fresh start only
	// the finest has anything. An empty chart on a system that is plainly
	// recording is the worse answer.
	s := open(t)
	now := time.Now()
	for i := 0; i < 30; i++ {
		if err := s.Record(now.Add(-time.Duration(i)*time.Minute), []Sample{{MetricTempFresh, 20}}); err != nil {
			t.Fatal(err)
		}
	}
	// A day-wide window would normally be answered from tier 1, which no
	// roll-up has filled.
	series, err := s.Query(MetricTempFresh, now.Add(-24*time.Hour), now, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Points) == 0 {
		t.Fatal("returned nothing although tier 0 held half an hour of readings")
	}
	if series.Resolution != Tiers[0].Name {
		t.Errorf("resolution = %q, want the finest tier it fell back to", series.Resolution)
	}
}

func TestChooseTierSkipsATierThatCannotReachBack(t *testing.T) {
	// Tier 0 keeps 48 hours. A three day window asked for at high detail must
	// not pick it, or the chart would simply begin a day late.
	now := time.Now()
	got := chooseTier(now.Add(-72*time.Hour), now, 1000000, now)
	if got.Level == 0 {
		t.Fatal("chose a tier whose retention does not cover the window")
	}
}

func TestSamplesFromSkipsAStaleState(t *testing.T) {
	// Writing the last known values again under a current timestamp draws a
	// flat line through an outage, which is the shape that hides one.
	if got := SamplesFrom(nil); got != nil {
		t.Error("produced samples from no state at all")
	}
}

func TestStatsDescribeWhatIsStored(t *testing.T) {
	s := open(t)
	now := time.Now().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		if err := s.Record(now.Add(time.Duration(i)*10*time.Second), []Sample{{MetricTempFresh, 20}}); err != nil {
			t.Fatal(err)
		}
	}
	st, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Rows[0] != 3 {
		t.Errorf("tier 0 rows = %d, want 3", st.Rows[0])
	}
	if st.SizeBytes <= 0 {
		t.Error("no database size reported")
	}
	if st.Oldest.IsZero() || st.Newest.IsZero() {
		t.Error("no time range reported")
	}
}

func TestSnapshotMakesAnOpenableCopyWhileWriting(t *testing.T) {
	// A file copied while it is being written to is one that may not open, and
	// the whole point of a backup is the day somebody needs it.
	s := open(t)
	now := time.Now().Add(-time.Hour)
	for i := 0; i < 50; i++ {
		if err := s.Record(now.Add(time.Duration(i)*10*time.Second),
			[]Sample{{MetricTempFresh, float64(i)}}); err != nil {
			t.Fatal(err)
		}
	}

	dst := filepath.Join(t.TempDir(), "copy.db")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_ = s.Record(time.Now(), []Sample{{MetricSetpoint, 22}})
		}
	}()
	if err := s.Snapshot(dst); err != nil {
		t.Fatal(err)
	}
	<-done

	copied, err := Open(dst)
	if err != nil {
		t.Fatalf("the copy will not open: %v", err)
	}
	defer copied.Close()

	series, err := copied.Query(MetricTempFresh, now.Add(-time.Hour), time.Now(), 10000)
	if err != nil {
		t.Fatal(err)
	}
	if len(series.Points) != 50 {
		t.Fatalf("the copy holds %d points, want 50", len(series.Points))
	}
}

func TestSnapshotReplacesAnExistingFile(t *testing.T) {
	// VACUUM INTO refuses to overwrite, and a backup that works once is not a
	// backup.
	s := open(t)
	if err := s.Record(time.Now(), []Sample{{MetricTempFresh, 1}}); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "copy.db")
	for i := 0; i < 3; i++ {
		if err := s.Snapshot(dst); err != nil {
			t.Fatalf("snapshot %d: %v", i+1, err)
		}
	}
}
