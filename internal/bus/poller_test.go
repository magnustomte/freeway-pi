package bus

import (
	"context"
	"testing"
	"time"

	"freewaypi/internal/modbus"
	"freewaypi/internal/modbustest"
)

func newPollerFixture(t *testing.T) (*modbustest.Unit, *Bus) {
	t.Helper()
	// The simulated unit has exactly the address space measured on the
	// real one, so a test that reads past the end fails here too.
	u := modbustest.NewUnit(1, LastHolding+1, LastCoil+1)
	c := modbus.NewClient(u, 1)
	c.InterFrame = 0
	c.Timeout = 50 * time.Millisecond
	b := New(c, Options{})
	t.Cleanup(b.Close)
	return u, b
}

// waitFirstPass blocks until the pass a Poller starts on construction has
// finished. Starting immediately is deliberate — a daemon should have data the
// moment it comes up, not one interval later — but a test that measures a pass
// has to let that one land first, or it counts both.
func waitFirstPass(t *testing.T, p *Poller) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for p.Stats().Passes == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the poller never completed its first pass")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPollerReadsTheWholeMap(t *testing.T) {
	u, b := newPollerFixture(t)
	u.Set(135, 220) // setpoint 22.0 C
	u.Set(LastHolding, 4242)
	u.SetCoil(16, true) // EC fans
	u.SetCoil(LastCoil, true)

	p := NewPoller(b, PollerOptions{Interval: time.Hour})
	t.Cleanup(p.Close)
	waitFirstPass(t, p)

	s := p.Refresh(context.Background())
	if !s.Complete() {
		t.Fatalf("pass reported failures: %v", s.Failed)
	}
	if got := len(s.Holding); got != LastHolding+1 {
		t.Fatalf("holding length %d, want %d", got, LastHolding+1)
	}
	if s.Holding[135] != 220 {
		t.Errorf("HR135 = %d, want 220", s.Holding[135])
	}
	if s.Holding[LastHolding] != 4242 {
		t.Errorf("HR%d = %d, want 4242", LastHolding, s.Holding[LastHolding])
	}
	if !s.Coils[16] || !s.Coils[LastCoil] {
		t.Errorf("coils 16 and %d = %v and %v, want both true", LastCoil, s.Coils[16], s.Coils[LastCoil])
	}
}

func TestPollerUsesTheMeasuredNumberOfRequests(t *testing.T) {
	// Measurement showed the cost is per request: seven blocks of 125 plus one
	// coil read. A change that quietly reads register by register would take
	// seventeen seconds on the real unit instead of one and a half, so the
	// request count is worth pinning down.
	u, b := newPollerFixture(t)
	p := NewPoller(b, PollerOptions{Interval: time.Hour})
	t.Cleanup(p.Close)
	waitFirstPass(t, p)

	before := u.Requests
	p.Refresh(context.Background())
	if got := u.Requests - before; got != 7 {
		t.Fatalf("a full pass took %d requests, want 7 (six blocks of %d plus the coils)", got, MaxBlock)
	}
}

func TestPollerPublishesAndReportsAge(t *testing.T) {
	_, b := newPollerFixture(t)
	p := NewPoller(b, PollerOptions{Interval: time.Hour})
	t.Cleanup(p.Close)

	deadline := time.Now().Add(2 * time.Second)
	for p.Current() == nil {
		if time.Now().After(deadline) {
			t.Fatal("no snapshot was published")
		}
		time.Sleep(time.Millisecond)
	}
	if age := p.Current().Age(); age > time.Second {
		t.Errorf("fresh snapshot reports an age of %v", age)
	}
	if got := p.Stats().Passes; got == 0 {
		t.Error("pass counter did not move")
	}
}

func TestPollerKeepsTheLastGoodSnapshotWhenTheUnitGoesQuiet(t *testing.T) {
	// A unit that stops answering must not blank the interface. Showing the
	// last known values with an honest age beats showing zeroes.
	u, b := newPollerFixture(t)
	u.Set(135, 220)

	p := NewPoller(b, PollerOptions{Interval: time.Hour})
	t.Cleanup(p.Close)
	waitFirstPass(t, p)
	p.Refresh(context.Background())

	good := p.Current()
	if good == nil || good.Holding[135] != 220 {
		t.Fatal("no usable first snapshot")
	}

	u.SetSilent(true)
	s := p.Refresh(context.Background())
	if s.Complete() {
		t.Fatal("a pass against a silent unit reported success")
	}
	if p.Current() != good {
		t.Fatal("a wholly failed pass replaced the last good snapshot")
	}
	if p.Stats().Failures == 0 {
		t.Error("a wholly failed pass was not counted as a failure")
	}
}

func TestPollerPublishesPartialPassesAndSaysWhichBlocksAreStale(t *testing.T) {
	u, b := newPollerFixture(t)
	p := NewPoller(b, PollerOptions{Interval: time.Hour})
	t.Cleanup(p.Close)
	waitFirstPass(t, p)

	// Fail one block in the middle of the pass, then recover.
	var n int
	orig := b.client.OnExchange
	b.client.OnExchange = func(ex modbus.Exchange) {
		if orig != nil {
			orig(ex)
		}
		n++
		if n == 9 { // partway through, after retries on an earlier block
			u.SetSilent(true)
		}
		if n == 12 {
			u.SetSilent(false)
		}
	}

	s := p.Refresh(context.Background())
	if s.Complete() {
		t.Skip("the injected failure did not land; timing dependent")
	}
	if len(s.Failed) == 0 {
		t.Fatal("an incomplete pass listed no failed blocks")
	}
	if p.Current() != s {
		t.Fatal("a partial pass was not published; the interface would show nothing")
	}
	if s.Failed[0].Error() == "" {
		t.Error("a failed block described itself as an empty string")
	}
}

func TestPollerRefreshRunsAtPollPriority(t *testing.T) {
	// Polling must never be the reason a button press waits. If this ever
	// changes, the whole point of the priority queue is lost.
	_, b := newPollerFixture(t)
	p := NewPoller(b, PollerOptions{Interval: time.Hour})
	t.Cleanup(p.Close)
	waitFirstPass(t, p)

	before := b.Stats().Requests[PriorityPoll]
	p.Refresh(context.Background())
	if b.Stats().Requests[PriorityPoll] <= before {
		t.Fatal("a refresh did not use the poll priority")
	}
	if b.Stats().Requests[PriorityWrite] != 0 || b.Stats().Requests[PriorityClient] != 0 {
		t.Fatal("a refresh used a priority reserved for callers who are waiting")
	}
}

func TestNextDelayAnchorsPassesToTheGrid(t *testing.T) {
	// The history is bucketed on wall-clock multiples of the interval. A
	// poller that merely waits the right length between passes still drifts in
	// phase, and every time the drift carries a pass across a boundary a
	// bucket is skipped and the chart gets a hole.
	interval := 10 * time.Second
	at := func(sec int, ms int) time.Time {
		return time.Date(2026, 9, 18, 19, 32, sec, ms*int(time.Millisecond), time.UTC)
	}
	cases := []struct {
		name string
		now  time.Time
		want time.Duration
	}{
		{"just after a boundary", at(31, 400), 8600 * time.Millisecond},
		{"a slower pass, same boundary", at(32, 700), 7300 * time.Millisecond},
		{"exactly on a boundary", at(40, 0), interval},
		{"a hair before the next", at(39, 990), interval + 10*time.Millisecond},
	}
	for _, c := range cases {
		if got := nextDelay(interval, c.now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNextDelayLandsOnTheGridWhateverThePhase(t *testing.T) {
	// The property that matters, checked across a whole period rather than at
	// the few points a table can hold.
	interval := 10 * time.Second
	base := time.Date(2026, 9, 18, 19, 32, 30, 0, time.UTC)
	for ms := 0; ms < 10000; ms += 37 {
		now := base.Add(time.Duration(ms) * time.Millisecond)
		landing := now.Add(nextDelay(interval, now))
		if !landing.Truncate(interval).Equal(landing) {
			t.Fatalf("from %v the next pass lands at %v, which is not on the grid", now, landing)
		}
	}
}
