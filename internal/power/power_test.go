package power

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"
)

// line is a kernel log record as dmesg prints it.
func line(secs float64, msg string) string {
	return fmt.Sprintf("[%12.6f] %s", secs, msg)
}

func watcher(source Source) *Watcher {
	return &Watcher{log: slog.New(slog.DiscardHandler), source: source}
}

func from(lines ...string) Source {
	return func(context.Context) ([]string, error) { return lines, nil }
}

func TestDetectionAndRecovery(t *testing.T) {
	w := watcher(from(
		line(13.860956, "hwmon hwmon2: Undervoltage detected!"),
		line(19.000000, "hwmon hwmon2: Voltage normalised"),
	))
	w.sample(context.Background())

	s := w.State()
	if !s.Watched {
		t.Fatal("not watched after a successful read")
	}
	if s.Now {
		t.Error("still under-voltage after the kernel said it was over")
	}
	// The count is the point: "it has happened" goes stale the moment it
	// recovers, and a supply that sags thirty times an hour is a different
	// problem from one that sagged once at boot.
	if s.Events != 1 {
		t.Errorf("events = %d after recovering, want 1", s.Events)
	}
	if s.First.IsZero() || s.Last.IsZero() {
		t.Error("the time it happened was not kept")
	}
}

func TestStillSaggingWhenTheLastWordIsDetection(t *testing.T) {
	w := watcher(from(
		line(13.8, "hwmon hwmon2: Undervoltage detected!"),
		line(19.0, "hwmon hwmon2: Voltage normalised"),
		line(25.0, "hwmon hwmon2: Undervoltage detected!"),
	))
	w.sample(context.Background())
	if s := w.State(); !s.Now || s.Events != 2 {
		t.Fatalf("now=%v events=%d, want true 2", s.Now, s.Events)
	}
}

// TestOtherKernelChatterIsIgnored: the kernel log is mostly other people's
// business, and a watcher that counted any line would report a healthy box as
// failing within a second of starting.
func TestOtherKernelChatterIsIgnored(t *testing.T) {
	w := watcher(from(
		line(1.0, "usb 1-1.4: new high-speed USB device number 5 using dwc_otg"),
		line(2.0, "mmc0: new high speed SDHC card at address aaaa"),
		line(3.0, "Bluetooth: hci0: BCM: chip id 94"),
	))
	w.sample(context.Background())
	if s := w.State(); s.Events != 0 || s.Now {
		t.Fatalf("chatter counted: events=%d now=%v", s.Events, s.Now)
	}
}

// TestAWrappedRingBufferDoesNotLookLikeRecovery: the buffer is finite. Once it
// wraps, a fresh count is lower than the last one, and reporting it would read
// as the problem improving while it was in fact getting worse.
func TestAWrappedRingBufferDoesNotLookLikeRecovery(t *testing.T) {
	var lines []string
	for i := 0; i < 5; i++ {
		lines = append(lines,
			line(float64(i)*30, "hwmon hwmon2: Undervoltage detected!"),
			line(float64(i)*30+5, "hwmon hwmon2: Voltage normalised"))
	}
	w := watcher(from(lines...))
	w.sample(context.Background())
	if s := w.State(); s.Events != 5 {
		t.Fatalf("events = %d, want 5", s.Events)
	}

	// The oldest three have fallen out of the buffer.
	w.source = from(lines[6:]...)
	w.sample(context.Background())
	if s := w.State(); s.Events != 5 {
		t.Fatalf("events = %d after the buffer wrapped, want 5", s.Events)
	}
}

// TestAFailedReadLeavesTheLastAnswerStanding: a helper that is momentarily busy
// must not turn a browning-out box into a healthy one for thirty seconds.
func TestAFailedReadLeavesTheLastAnswerStanding(t *testing.T) {
	w := watcher(from(line(13.8, "hwmon hwmon2: Undervoltage detected!")))
	w.sample(context.Background())

	w.source = func(context.Context) ([]string, error) { return nil, errors.New("busy") }
	w.sample(context.Background())

	if s := w.State(); s.Events != 1 || !s.Watched {
		t.Fatalf("events = %d watched = %v after a failed read, want 1 true", s.Events, s.Watched)
	}
}

// TestDrainReportsWhatHappenedInBetween: the metric is one sample per bucket
// saying whether the supply sagged during it, not what was true at the instant
// the sample was taken.
func TestDrainReportsWhatHappenedInBetween(t *testing.T) {
	w := watcher(from(line(13.8, "hwmon hwmon2: Undervoltage detected!"),
		line(14.0, "hwmon hwmon2: Voltage normalised")))
	w.sample(context.Background())

	if !w.Drain() {
		t.Fatal("the first drain missed an event that had happened")
	}
	if w.Drain() {
		t.Fatal("the same event was reported twice")
	}
	w.source = from(line(13.8, "hwmon hwmon2: Undervoltage detected!"),
		line(14.0, "hwmon hwmon2: Voltage normalised"),
		line(44.0, "hwmon hwmon2: Undervoltage detected!"))
	w.sample(context.Background())
	if !w.Drain() {
		t.Fatal("a new event was not reported")
	}
}

// TestTimestampsBecomeTimesOfDay: the number in the line is seconds since boot.
// Left as it is, a line from the ring buffer would be stamped with the moment
// it was read rather than the moment it happened, and every event since boot
// would appear to have arrived at once.
func TestTimestampsBecomeTimesOfDay(t *testing.T) {
	up, err := uptime()
	if err != nil {
		t.Skip("no /proc/uptime here")
	}
	if up < 2*time.Second {
		t.Skip("booted moments ago")
	}
	w := watcher(from(line(1.0, "hwmon hwmon2: Undervoltage detected!")))
	w.sample(context.Background())

	ago := time.Since(w.State().Last)
	want := up - time.Second
	if d := ago - want; d < -2*time.Second || d > 2*time.Second {
		t.Fatalf("the event landed %s ago, want about %s", ago.Round(time.Second), want.Round(time.Second))
	}
}
