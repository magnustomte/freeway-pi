package control

import (
	"testing"
	"time"

	"freewaypi/internal/bus"
	"freewaypi/internal/eda"
	"freewaypi/internal/modbus"
	"freewaypi/internal/modbustest"
)

func newUnit(t *testing.T) (*modbustest.Unit, *Unit) {
	t.Helper()
	u := modbustest.NewUnit(1, bus.LastHolding+1, bus.LastCoil+1)
	c := modbus.NewClient(u, 1)
	c.InterFrame = 0
	c.Timeout = 20 * time.Millisecond
	c.Retries = 0
	b := bus.New(c, bus.Options{})
	t.Cleanup(b.Close)
	p := bus.NewPoller(b, bus.PollerOptions{Interval: time.Hour})
	t.Cleanup(p.Close)
	return u, New(b, p, time.Second, "Loft")
}

func waitForState(t *testing.T, u *Unit) *eda.State {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := u.State()
		if err == nil {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("no state: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLinkReportsConnectedOnAHealthyLine(t *testing.T) {
	_, unit := newUnit(t)
	st := waitForState(t, unit)

	if st.Link.Status != eda.LinkOK {
		t.Fatalf("status = %q (%s), want ok", st.Link.Status, st.Link.Detail)
	}
	if st.Link.Label == "" {
		t.Error("no label for the interface to show")
	}
	if st.Name != "Loft" {
		t.Errorf("name = %q, want the configured one", st.Name)
	}
}

func TestLinkReportsDisconnectedWhenTheSnapshotGoesStale(t *testing.T) {
	_, unit := newUnit(t)
	waitForState(t, unit)

	// Nothing has refreshed for far longer than the staleness threshold.
	unit.staleAfter = time.Nanosecond
	st := waitForState(t, unit)

	if st.Link.Status != eda.LinkDown {
		t.Fatalf("status = %q, want down", st.Link.Status)
	}
	if st.Link.Detail == "" {
		t.Error("said the link was down without saying why")
	}
}

func TestLinkReportsDegradedWhenTheLineIsNoisy(t *testing.T) {
	// A connector working loose shows up as a rising error rate long before it
	// shows up as silence, which is the whole reason for a middle state.
	u, unit := newUnit(t)
	// This is about the path from the wire to the status, not about how long
	// a bad rate has to last; that has tests of its own in control_test.go.
	unit.degradeAfter = 0
	waitForState(t, unit)

	// Enough attempts for the rate to be judged at all: below a quarter of the
	// window a single failure would read as a fault, so nothing is called
	// unstable there. See bus.EnoughSamples.
	u.SetSilent(true)
	for i := 0; i < 100; i++ {
		_ = unit.poller.Reread(t.Context(), bus.PriorityPoll, true, 0, 1)
	}
	u.SetSilent(false)
	_ = unit.poller.Reread(t.Context(), bus.PriorityPoll, true, 0, 1)

	st := waitForState(t, unit)
	if st.Link.Status != eda.LinkDegraded {
		t.Fatalf("status = %q with a %.0f%% error rate, want degraded",
			st.Link.Status, st.Link.ErrorRate*100)
	}
	if st.Link.ErrorRate <= eda.DegradedErrorRate {
		t.Errorf("error rate = %v, expected it above the threshold", st.Link.ErrorRate)
	}
}

// TestAFreshDaemonIsNotCalledUnstable: the rate is the share of the last few
// hundred attempts, and a daemon that has just started has made a few dozen.
// One unlucky exchange among them reads as nearly two per cent — which raised
// an alarm about a healthy line every time the service was deployed.
func TestAFreshDaemonIsNotCalledUnstable(t *testing.T) {
	u, unit := newUnit(t)
	waitForState(t, unit)

	// A handful of attempts, all of them failing: as high a rate as there is,
	// and still nothing to draw a conclusion from.
	u.SetSilent(true)
	for i := 0; i < 5; i++ {
		_ = unit.poller.Reread(t.Context(), bus.PriorityPoll, true, 0, 1)
	}
	u.SetSilent(false)
	_ = unit.poller.Reread(t.Context(), bus.PriorityPoll, true, 0, 1)

	st := waitForState(t, unit)
	if st.Link.Status == eda.LinkDegraded {
		t.Errorf("a line was called unstable on %.0f %% of a handful of attempts",
			st.Link.ErrorRate*100)
	}
}
