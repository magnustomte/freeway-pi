package hostctl

import (
	"testing"
	"time"
)

// TestParseRebootAtFallsBackToFour: an empty or malformed setting must not mean
// "never" or "now" — the first leaves a box needing a restart for ever, and the
// second restarts a house's ventilation at whatever moment the file was read.
func TestParseRebootAtFallsBackToFour(t *testing.T) {
	for _, c := range []struct {
		in   string
		h, m int
	}{
		{"", 4, 0},
		{"  ", 4, 0},
		{"nonsense", 4, 0},
		{"25:00", 4, 0},
		{"03:30", 3, 30},
		{" 23:15 ", 23, 15},
		{"00:00", 0, 0},
	} {
		h, m := ParseRebootAt(c.in)
		if h != c.h || m != c.m {
			t.Errorf("ParseRebootAt(%q) = %02d:%02d, want %02d:%02d", c.in, h, m, c.h, c.m)
		}
	}
}

// TestDueAtIsAWindow: this is checked on a timer, so an exact comparison would
// miss the minute whenever a check landed either side of it — and the box would
// wait another day, every day.
func TestDueAtIsAWindow(t *testing.T) {
	at := func(h, m int) time.Time {
		return time.Date(2026, 9, 21, h, m, 0, 0, time.UTC)
	}
	window := 16 * time.Minute

	for _, c := range []struct {
		now  time.Time
		want bool
		why  string
	}{
		{at(4, 0), true, "exactly on the hour"},
		{at(4, 10), true, "a check that landed ten minutes late"},
		{at(4, 15), true, "the last minute of the window"},
		{at(4, 16), false, "past the window"},
		{at(3, 59), false, "a minute early"},
		{at(23, 0), false, "the other end of the day"},
	} {
		if got := DueAt(c.now, 4, 0, window); got != c.want {
			t.Errorf("DueAt(%s) = %v, want %v — %s", c.now.Format("15:04"), got, c.want, c.why)
		}
	}
}

// TestMidnightIsNotEveryHour: a window that wrapped would fire at 00:00 and
// then again whenever the arithmetic went negative.
func TestMidnightIsNotEveryHour(t *testing.T) {
	window := 16 * time.Minute
	for h := 0; h < 24; h++ {
		now := time.Date(2026, 9, 21, h, 30, 0, 0, time.UTC)
		if DueAt(now, 0, 0, window) {
			t.Errorf("a midnight window fired at %02d:30", h)
		}
	}
}

// TestRebootWindowKeepsThePromise: the time in the interface is a time, not a
// hint. The window used to be derived from the poll interval, which made
// "restart at 14:40" mean anywhere inside the following quarter of an hour
// depending on when the daemon happened to have started.
func TestRebootWindowKeepsThePromise(t *testing.T) {
	at := func(h, m, s int) time.Time {
		return time.Date(2026, 9, 21, h, m, s, 0, time.UTC)
	}
	for _, c := range []struct {
		now  time.Time
		want bool
		why  string
	}{
		{at(14, 40, 0), true, "on the minute"},
		{at(14, 41, 20), true, "a check that landed late, still inside"},
		{at(14, 42, 0), false, "two minutes late is not 14:40"},
		{at(14, 55, 0), false, "a quarter of an hour late is the old bug"},
	} {
		if got := DueAt(c.now, 14, 40, RebootWindow); got != c.want {
			t.Errorf("DueAt(%s, window=%s) = %v, want %v — %s",
				c.now.Format("15:04:05"), RebootWindow, got, c.want, c.why)
		}
	}
}
