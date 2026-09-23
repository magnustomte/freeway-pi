package control

import (
	"sync"
	"time"

	"freewaypi/internal/eda"
)

// timedModeTracker works out how long a timed mode has left.
//
// The unit cannot tell us. Holding register 56 is named "time left" in the
// register list but is in fact the duration the unit acts on, and it does not
// count down — verified on a live unit. So the countdown is ours:
// we watch the coil turn on and measure from there.
//
// Started here, that is exact. Started from the panel while nothing was
// watching, we only know it was already running when we first looked, and say
// so rather than inventing a number.
//
// Boost works the same way and is counted by the same code; the manual calls
// it forcering, and it runs for holding register 66 minutes at register 67's
// level.
type timedModeTracker struct {
	mu      sync.Mutex
	since   time.Time
	minutes int
	// known is false when the run was already in progress the first time we
	// saw it, so its start is genuinely unknown.
	known bool
	// mode is which timed mode is being counted, so a boost started straight
	// after an overpressure does not inherit the earlier one's duration.
	mode eda.Mode
}

// observe is called for every decoded state and notices the transitions.
func (t *timedModeTracker) observe(st *eda.State, firstLook bool) {
	// Only these two run for a set time and then stop by themselves.
	var minutes int
	switch st.Mode {
	case eda.ModeOverpressure:
		minutes = st.OverpressureMinutes
	case eda.ModeBoost:
		minutes = st.BoostMinutes
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	switch {
	case minutes > 0 && st.Mode != t.mode:
		t.since = time.Now()
		t.minutes = minutes
		t.mode = st.Mode
		// A run that was already going when we started cannot be timed.
		t.known = !firstLook
	case minutes == 0:
		t.known = false
		t.since = time.Time{}
		t.mode = ""
	}
}

// remaining returns how long is left, and whether that is known at all.
func (t *timedModeTracker) remaining() (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.known || t.since.IsZero() {
		return 0, false
	}
	left := time.Duration(t.minutes)*time.Minute - time.Since(t.since)
	if left < 0 {
		// The unit stops itself; if the coil is still set past the duration,
		// our idea of when it began was wrong and pretending otherwise helps
		// nobody.
		return 0, false
	}
	return left, true
}
