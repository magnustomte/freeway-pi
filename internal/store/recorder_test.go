package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"freewaypi/internal/eda"
)

func TestRecorderStoresEachSnapshotOnce(t *testing.T) {
	// The poller and the recorder run at their own rates, so the same snapshot
	// is usually seen several times. Writing it again is waste on a card that
	// wears out.
	s, err := Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	var mu sync.Mutex
	taken := time.Now().Add(-time.Hour)
	state := func() (*eda.State, error) {
		mu.Lock()
		defer mu.Unlock()
		return &eda.State{Taken: taken, Setpoint: 22}, nil
	}

	r := NewRecorder(s, state, RecorderOptions{Interval: 5 * time.Millisecond, Housekeeping: time.Hour})
	t.Cleanup(r.Close)

	deadline := time.Now().Add(2 * time.Second)
	for r.Stats().Written == 0 {
		if time.Now().After(deadline) {
			t.Fatal("nothing was written")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond) // many more ticks, same snapshot

	if got := r.Stats().Written; got != 1 {
		t.Fatalf("wrote the same snapshot %d times, want 1", got)
	}

	mu.Lock()
	taken = taken.Add(10 * time.Second)
	mu.Unlock()

	deadline = time.Now().Add(2 * time.Second)
	for r.Stats().Written < 2 {
		if time.Now().After(deadline) {
			t.Fatal("a new snapshot was not written")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRecorderSkipsStaleStatesRatherThanDrawingAFlatLine(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	var mu sync.Mutex
	taken := time.Now().Add(-time.Hour)
	state := func() (*eda.State, error) {
		mu.Lock()
		defer mu.Unlock()
		taken = taken.Add(10 * time.Second)
		return &eda.State{Taken: taken, Stale: true, Setpoint: 22}, nil
	}

	r := NewRecorder(s, state, RecorderOptions{Interval: 5 * time.Millisecond, Housekeeping: time.Hour})
	t.Cleanup(r.Close)

	deadline := time.Now().Add(2 * time.Second)
	for r.Stats().Skipped < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("stale states were not skipped: %+v", r.Stats())
		}
		time.Sleep(time.Millisecond)
	}
	if got := r.Stats().Written; got != 0 {
		t.Fatalf("wrote %d stale states; an outage should leave a gap, not a flat line", got)
	}
}

func TestRecorderSurvivesAStateThatCannotBeRead(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	r := NewRecorder(s, func() (*eda.State, error) { return nil, errors.New("no snapshot yet") },
		RecorderOptions{Interval: 2 * time.Millisecond, Housekeeping: 3 * time.Millisecond})
	t.Cleanup(r.Close)

	time.Sleep(60 * time.Millisecond)
	if st := r.Stats(); st.Written != 0 || st.LastErr != "" {
		t.Fatalf("stats = %+v, want nothing written and no error recorded", st)
	}
}
