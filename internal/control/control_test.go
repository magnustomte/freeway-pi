package control

import (
	"testing"
	"time"

	"freewaypi/internal/eda"
)

// TestDisplayNameFallsBackToTheModel: a unit that has never been given a name
// should say what it is rather than the word "Aggregat", because the unit does
// know — HR597 holds a code for the model.
func TestDisplayNameFallsBackToTheModel(t *testing.T) {
	u := &Unit{}
	u.SetName("")

	withModel := &eda.State{Machine: eda.Machine{Family: "Pelican"}}
	if got := u.displayName(withModel); got != "Pelican" {
		t.Errorf("unnamed unit = %q, want the model it reported", got)
	}

	// A name that was given wins over the model.
	u.SetName("Ventilasjon")
	if got := u.displayName(withModel); got != "Ventilasjon" {
		t.Errorf("named unit = %q, want the name", got)
	}

	// And a unit whose model is not in the list still has to be called
	// something.
	u.SetName("")
	if got := u.displayName(&eda.State{}); got != "Aggregat" {
		t.Errorf("unknown model = %q, want a last resort", got)
	}
}

// TestTheUnstableWarningDoesNotBlink: the error rate is the share of the last
// 256 attempts, which is about seven minutes — so a line sitting near the mark
// crosses it repeatedly as failures age out of the window. With one threshold
// that was a warning appearing and clearing every few minutes, and a warning
// that blinks is one people stop reading.
//
// The sequence here is one that was measured: 2.34 %, then 1.56 % as the
// window cleared, then 0.78 %. The middle reading is below the mark and is not
// yet healthy.
func TestTheUnstableWarningDoesNotBlink(t *testing.T) {
	u := &Unit{}
	for _, c := range []struct {
		rate float64
		want bool
		why  string
	}{
		{0.005, false, "a healthy line"},
		{0.023, true, "past the mark"},
		{0.016, true, "below the mark but not yet better"},
		{0.011, true, "still not better"},
		{0.008, false, "actually better"},
		{0.016, false, "wobbling upward is not a fault either"},
		{0.021, true, "past the mark again"},
	} {
		if got := u.unstable(c.rate, 256); got != c.want {
			t.Errorf("at %.1f %%: unstable = %v, want %v — %s", c.rate*100, got, c.want, c.why)
		}
	}
}

// TestOneThresholdWouldHaveBlinked: the point of the second threshold, stated
// as the thing it prevents. Without it the same sequence changes state four
// times instead of two.
func TestOneThresholdWouldHaveBlinked(t *testing.T) {
	sequence := []float64{0.005, 0.023, 0.016, 0.021, 0.016, 0.008}

	u := &Unit{}
	changes, last := 0, false
	for _, r := range sequence {
		if got := u.unstable(r, 256); got != last {
			changes++
			last = got
		}
	}
	if changes != 2 {
		t.Errorf("the warning changed state %d times, want 2 (on and off once)", changes)
	}

	// The same readings against a single threshold, which is what this replaced.
	naive, l2 := 0, false
	for _, r := range sequence {
		if got := r > 0.02; got != l2 {
			naive++
			l2 = got
		}
	}
	if naive <= changes {
		t.Fatalf("one threshold gave %d changes and two gave %d; the test is not testing anything", naive, changes)
	}
}

// TestABurstIsNotAnUnstableLine: the sequence below is what set the warning
// off one morning, a reading a minute — six failed requests around the unit
// switching to heat recovery, all of them retried, on a line that had been
// under one per cent all night. Past the mark for one reading. Not a fault.
func TestABurstIsNotAnUnstableLine(t *testing.T) {
	u := &Unit{degradeAfter: 5 * time.Minute}
	start := time.Date(2026, 9, 23, 10, 15, 0, 0, time.UTC)
	for i, rate := range []float64{0, 0.0078, 0.0078, 0.0078, 0.0078, 0.0078,
		0.0078, 0.0078, 0.0234, 0.0156, 0.0156, 0.0156, 0.0156, 0.0156, 0.0078} {
		if u.unstableAt(start.Add(time.Duration(i)*time.Minute), rate, 256) {
			t.Fatalf("called unstable at minute %d (%.2f %%) by a burst that lasted one reading", i, rate*100)
		}
	}
}

// TestALooseConnectorIsStillCaught: what the warning is for. A rate that
// climbs and stays is reported once it has stayed, and not before.
func TestALooseConnectorIsStillCaught(t *testing.T) {
	u := &Unit{degradeAfter: 5 * time.Minute}
	start := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	for m := 0; m <= 7; m++ {
		got := u.unstableAt(start.Add(time.Duration(m)*time.Minute), 0.035, 256)
		if want := m >= 5; got != want {
			t.Errorf("minute %d at 3.5 %%: unstable = %v, want %v", m, got, want)
		}
	}
}

// TestDippingBelowTheMarkStartsTheWaitAgain: the question is whether the line
// has become bad, so a rate that crosses the mark, falls back and crosses it
// again has not been past it for five minutes, however long it has wobbled.
func TestDippingBelowTheMarkStartsTheWaitAgain(t *testing.T) {
	u := &Unit{degradeAfter: 5 * time.Minute}
	start := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	at := func(m int) time.Time { return start.Add(time.Duration(m) * time.Minute) }
	u.unstableAt(at(0), 0.03, 256)
	u.unstableAt(at(4), 0.03, 256)
	u.unstableAt(at(5), 0.015, 256) // below the mark: the wait starts over
	if u.unstableAt(at(6), 0.03, 256) {
		t.Fatal("called unstable one minute after the rate came back over the mark")
	}
	if !u.unstableAt(at(11), 0.03, 256) {
		t.Fatal("not called unstable after five further minutes over the mark")
	}
}
