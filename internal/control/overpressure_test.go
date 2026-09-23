package control

import (
	"testing"
	"time"

	"freewaypi/internal/eda"
)

func on(minutes int) *eda.State {
	return &eda.State{Mode: eda.ModeOverpressure, OverpressureMinutes: minutes}
}

func boosting(minutes int) *eda.State {
	return &eda.State{Mode: eda.ModeBoost, BoostMinutes: minutes}
}
func off() *eda.State { return &eda.State{Mode: eda.ModeNormal} }

func TestOverpressureCountsFromWhenItSawTheRunStart(t *testing.T) {
	var tr timedModeTracker
	tr.observe(off(), true)
	tr.observe(on(20), false)

	left, known := tr.remaining()
	if !known {
		t.Fatal("a run we watched start has no countdown")
	}
	if left < 19*time.Minute || left > 20*time.Minute {
		t.Fatalf("remaining = %v, want just under 20 minutes", left)
	}
}

func TestOverpressureAdmitsWhenItDoesNotKnow(t *testing.T) {
	// Started from the panel while nothing was watching. The unit cannot tell
	// us when: register 56 is the duration and does not count down. Saying so
	// is better than inventing a number.
	var tr timedModeTracker
	tr.observe(on(20), true)

	if _, known := tr.remaining(); known {
		t.Fatal("claimed to know the remaining time of a run it never saw start")
	}
}

func TestOverpressureForgetsWhenTheRunEnds(t *testing.T) {
	var tr timedModeTracker
	tr.observe(off(), true)
	tr.observe(on(20), false)
	tr.observe(off(), false)

	if _, known := tr.remaining(); known {
		t.Fatal("still counting down after the run ended")
	}
}

func TestOverpressureRestartsTheClockOnASecondRun(t *testing.T) {
	var tr timedModeTracker
	tr.observe(off(), true)
	tr.observe(on(5), false)
	tr.observe(off(), false)
	tr.observe(on(60), false)

	left, known := tr.remaining()
	if !known {
		t.Fatal("no countdown for the second run")
	}
	if left < 59*time.Minute {
		t.Fatalf("remaining = %v, want just under 60 minutes; the first run's duration leaked", left)
	}
}

func TestOverpressureStopsClaimingOnceThePredictionHasExpired(t *testing.T) {
	// If the coil is still set past the duration, our idea of when it began
	// was wrong, and a negative countdown helps nobody.
	var tr timedModeTracker
	tr.observe(off(), true)
	tr.observe(on(20), false)

	tr.mu.Lock()
	tr.since = time.Now().Add(-25 * time.Minute)
	tr.mu.Unlock()

	if _, known := tr.remaining(); known {
		t.Fatal("reported a remaining time for a run that should have ended")
	}
}

func TestBoostIsCountedTheSameWay(t *testing.T) {
	// The manual calls it forcering. It runs for a set time and the unit
	// reports no more about how long is left than it does for overpressure.
	var tr timedModeTracker
	tr.observe(off(), true)
	tr.observe(boosting(30), false)

	left, known := tr.remaining()
	if !known {
		t.Fatal("a boost we watched start has no countdown")
	}
	if left < 29*time.Minute || left > 30*time.Minute {
		t.Fatalf("remaining = %v, want just under 30 minutes", left)
	}
}

func TestSwitchingBetweenTimedModesRestartsTheCount(t *testing.T) {
	// A boost started straight after an overpressure must not inherit the
	// earlier one's duration.
	var tr timedModeTracker
	tr.observe(off(), true)
	tr.observe(on(5), false)
	tr.observe(boosting(60), false)

	left, known := tr.remaining()
	if !known {
		t.Fatal("no countdown after switching mode")
	}
	if left < 59*time.Minute {
		t.Fatalf("remaining = %v, want just under 60; the overpressure duration leaked", left)
	}
}
