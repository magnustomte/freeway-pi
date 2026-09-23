package web

import (
	"testing"
	"time"
)

// TestClockSuspect: three clocks, and which of them is out. The cases are the
// ones that happened: a freshly flashed box on the image's London timezone
// beside a unit and a reader in Oslo.
func TestClockSuspect(t *testing.T) {
	oslo := time.Date(2026, 9, 23, 10, 25, 0, 0, time.Local)
	for _, c := range []struct {
		why               string
		unit, box, reader time.Time
		wantSuspect       bool
	}{
		{"the box is on London, the unit and the reader agree",
			oslo, oslo.Add(-time.Hour), oslo, true},
		{"the unit missed summer time, the box is right",
			oslo.Add(-time.Hour), oslo, oslo, false},
		{"all three agree, give or take a poll",
			oslo.Add(40 * time.Second), oslo, oslo.Add(-20 * time.Second), false},
		{"the unit has drifted a quarter of an hour, the box is right",
			oslo.Add(15 * time.Minute), oslo, oslo, false},
		{"everything disagrees, so there is nothing to go on",
			oslo.Add(-2 * time.Hour), oslo.Add(-time.Hour), oslo, false},
	} {
		got, _ := clockSuspect(c.unit, c.box, c.reader)
		if got != c.wantSuspect {
			t.Errorf("%s: suspect = %v, want %v", c.why, got, c.wantSuspect)
		}
	}
}

// TestReaderWallHasNoZone: the browser sends the digits of its local time.
// Anything else — a zone, a date that is not one — is no opinion, and the
// write goes ahead as it always did.
func TestReaderWallHasNoZone(t *testing.T) {
	if _, ok := readerWall("2026-09-23T10:25:00"); !ok {
		t.Error("a plain local time was not accepted")
	}
	for _, bad := range []string{"", "2026-09-23T10:25:00Z", "2026-09-23 10:25", "i dag"} {
		if _, ok := readerWall(bad); ok {
			t.Errorf("%q was accepted as a reader's clock", bad)
		}
	}
}
