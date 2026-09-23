// Package control applies a person's intent to the unit.
package control

import (
	"context"
	"sync/atomic"
	"time"

	"freewaypi/internal/i18n"

	"freewaypi/internal/bus"
	"freewaypi/internal/eda"
	"freewaypi/internal/modbus"
)

// Unit is the thing the interface talks to.
type Unit struct {
	bus    *bus.Bus
	poller *bus.Poller
	// staleAfter is when a snapshot stops being worth presenting as current.
	// Three poll intervals: one missed pass is a hiccup, three is a problem.
	staleAfter time.Duration

	// name is what the interface calls it, and may be changed while running.
	name  atomic.Pointer[string]
	timed timedModeTracker
	seen  atomic.Bool
	// clockSettable records what was learnt by trying. Unknown until then,
	// because a write to the setting block is answered the same way whether
	// the board acts on it or not, and only this one has been measured.
	clockSettable atomic.Pointer[bool]
	// syncing is held while a clock write waits for its minute boundary, so
	// two presses do not both land.
	syncing atomic.Bool
	// degraded remembers whether the line is currently being called unstable,
	// so recovering takes a lower rate than falling did. See the two
	// thresholds in eda.
	degraded atomic.Bool
	// overSince is when the rate last went past the mark, in Unix nanoseconds,
	// or zero while it is not past it.
	overSince atomic.Int64
	// degradeAfter is eda.DegradedAfter, and a field so a test of the whole
	// path does not have to wait five minutes to see a noisy line reported.
	degradeAfter time.Duration
}

// New returns a Unit. name is what the interface calls it.
func New(b *bus.Bus, p *bus.Poller, pollInterval time.Duration, name string) *Unit {
	u := &Unit{bus: b, poller: p, staleAfter: 3 * pollInterval, degradeAfter: eda.DegradedAfter}
	u.SetName(name)
	return u
}

// SetName changes what the interface calls this unit.
//
// Empty means "whatever it turns out to be": the unit says which model it is,
// and a box that has never been given a name should show that rather than the
// word "Aggregat". Somebody with two of them can still name them.
func (u *Unit) SetName(name string) {
	u.name.Store(&name)
}

// displayName is the name that was given, or what the unit says it is, or a
// last resort for a unit that answers with a model code nobody has a name for.
func (u *Unit) displayName(st *eda.State) string {
	if n := u.name.Load(); n != nil && *n != "" {
		return *n
	}
	if st.Machine.Family != "" {
		return st.Machine.Family
	}
	return "Aggregat"
}

// State returns the decoded state of the most recent snapshot.
func (u *Unit) State() (*eda.State, error) {
	st, err := eda.Decode(u.poller.Current(), u.staleAfter)
	if err != nil {
		return nil, err
	}
	st.Name = u.displayName(st)
	st.Link = u.link(st)
	st.ClockSettable = u.clockSettable.Load()
	u.timed.observe(st, !u.seen.Swap(true))
	if left, ok := u.timed.remaining(); ok {
		secs := left.Seconds()
		st.ModeRemaining = &secs
	}
	return st, nil
}

// link summarises whether we are talking to the unit, for the dot the interface
// shows. Three states rather than two, because "answering, but not reliably" is
// a real condition and the one worth catching early: a connector working loose
// shows up as a rising error rate long before it shows up as silence.
func (u *Unit) link(st *eda.State) eda.Link {
	stats := u.bus.Stats()
	rate := stats.RecentErrorRate
	switch {
	// Ids rather than sentences: which language these are read in depends on
	// who is reading, and that is not known here.
	case st.Stale:
		return eda.Link{Status: eda.LinkDown, Label: "link.down", Detail: "link.down.detail", ErrorRate: rate}
	case st.Incomplete:
		return eda.Link{Status: eda.LinkDegraded, Label: "link.degraded", Detail: "link.incomplete.detail", ErrorRate: rate}
	case u.unstable(rate, stats.RecentSamples):
		return eda.Link{Status: eda.LinkDegraded, Label: "link.degraded",
			Detail: "link.errorrate.detail", DetailArg: rate * 100, ErrorRate: rate}
	default:
		return eda.Link{Status: eda.LinkOK, Label: "link.ok", ErrorRate: rate}
	}
}

// Settings reads the named settings out of the latest snapshot. No bus traffic:
// the poller has already read every register they live in.
func (u *Unit) Settings() ([]eda.SettingValue, error) {
	snap := u.poller.Current()
	if snap == nil {
		return nil, i18n.Errf("error.unit.notread")
	}
	return eda.ReadSettings(snap.Holding, snap.Coils)
}

// Alarms reads the unit's alarm log out of the latest snapshot.
func (u *Unit) Alarms() ([]eda.Alarm, error) {
	snap := u.poller.Current()
	if snap == nil {
		return nil, i18n.Errf("error.unit.notread")
	}
	return eda.ReadAlarms(snap.Holding)
}

// Programs reads the timer programs out of the latest snapshot.
func (u *Unit) Programs() ([]eda.WeekProgram, []eda.YearProgram, error) {
	snap := u.poller.Current()
	if snap == nil {
		return nil, nil, i18n.Errf("error.unit.notread")
	}
	week, err := eda.ReadWeekPrograms(snap.Holding)
	if err != nil {
		return nil, nil, err
	}
	year, err := eda.ReadYearPrograms(snap.Holding)
	if err != nil {
		return nil, nil, err
	}
	return week, year, nil
}

// SyncClock sets the unit's clock from system time, and checks that it stuck.
//
// The check is not optional here. This unit acknowledges a write to the clock
// registers and does not apply it: the value lands in the register mirror, the
// unit's own clock task overwrites it, and a read taken within a few hundred
// milliseconds still shows the value that was written. An immediate read-back
// therefore reports success for a write that did nothing, which is how this was
// first reported as working.
func (u *Unit) SyncClock(ctx context.Context) error {
	// One at a time. Two presses would otherwise both wait for the same
	// boundary and write within milliseconds of each other.
	if !u.syncing.CompareAndSwap(false, true) {
		return i18n.Errf("error.clock.busy")
	}
	defer u.syncing.Store(false)

	// Wait for the boundary. The setting block has no seconds register and the
	// unit zeroes the seconds when it applies the write, so a write sent at an
	// arbitrary moment leaves the clock up to a minute behind — measured: a
	// write sent just after a minute began read back as that minute with the
	// seconds thrown away. Landing it just before a minute begins makes the
	// zeroed seconds the right ones.
	target := NextClockWrite(time.Now())
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Until(target.Add(-clockWriteLead))):
	}

	writes := eda.SetClock(eda.Time{
		Minute: target.Minute(), Hour: target.Hour(),
		Day: target.Day(), Month: int(target.Month()), Year: target.Year(),
	})
	if err := u.Apply(ctx, writes); err != nil {
		return err
	}

	// Long enough for the unit's own clock task to have had its say. The
	// lesson that made this necessary still holds: an immediate read-back
	// proves the register took a value, not that the unit acted on it.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(clockSettleDelay):
	}
	// Read straight off the bus rather than through the snapshot. A snapshot
	// carries one timestamp for a pass that takes a second and a half, and
	// Reread deliberately does not refresh it — so checking a clock write
	// through it would measure the age of the last poll as if it were the
	// unit's error, and fail a write that had worked.
	unitClock, readAt, err := u.readClock(ctx)
	if err != nil {
		return err
	}
	drift := readAt.Sub(unitClock)
	if drift < 0 {
		drift = -drift
	}

	// Published too, so the interface stops showing the old time without
	// waiting for the next pass.
	if err := u.poller.Reread(ctx, bus.PriorityWrite, false, eda.HRSeconds, 7); err != nil {
		return err
	}

	if unitClock.IsZero() || drift > clockAcceptableDrift {
		no := false
		u.clockSettable.Store(&no)
		return i18n.Errf("error.clock.unchanged", drift.Round(time.Second))
	}
	yes := true
	u.clockSettable.Store(&yes)
	return nil
}

// readClock reads the running clock and the instant it was read.
//
// The instant matters: the whole question afterwards is how far the unit is
// from the truth, and a reading timestamped a second late is a second of error
// that is not the unit's.
func (u *Unit) readClock(ctx context.Context) (clock, at time.Time, err error) {
	var regs []uint16
	err = u.bus.Do(ctx, bus.PriorityWrite, func(c *modbus.Client) error {
		r, err := c.ReadHolding(eda.HRSeconds, 7)
		at = time.Now()
		regs = r
		return err
	})
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return eda.DecodeClockBlock(regs), at, nil
}

// NextClockWrite is the minute the next clock write will set.
//
// Exported so the interface can say how long the wait will be rather than
// leaving a button apparently doing nothing for the best part of a minute.
func NextClockWrite(now time.Time) time.Time {
	target := now.Truncate(time.Minute).Add(time.Minute)
	// Too close to prepare for: take the one after. The margin is the lead
	// plus room for the bus to be busy with a poll when the moment comes.
	if target.Sub(now) < clockWriteLead+250*time.Millisecond {
		target = target.Add(time.Minute)
	}
	return target
}

const (
	// clockSettleDelay is how long to wait before believing a clock write.
	// The first measurements used half a second for the same kind of check,
	// and were right to.
	clockSettleDelay = 2 * time.Second
	// clockAcceptableDrift is how far out the clock may still be afterwards.
	// Tight, because a write timed to the boundary lands within a second:
	// anything worse than a few seconds means it did not take.
	clockAcceptableDrift = 10 * time.Second
	// clockWriteLead is how early to send it, so the unit applies the write as
	// the minute begins rather than after it. Measured turnaround on this bus
	// is well under 100 ms; the rest is margin.
	clockWriteLead = 250 * time.Millisecond
)

// Apply performs the writes in order and then re-reads exactly what was
// written, so the answer to the request that caused them already reflects them.
//
// Writes go at the highest priority: somebody is looking at a screen waiting
// for the button they pressed to do something.
func (u *Unit) Apply(ctx context.Context, writes []eda.Write) error {
	for _, w := range writes {
		if err := u.write(ctx, w); err != nil {
			// Not prefixed with what was being written: that description is in one
			// language, and it is already in the log line beside this.
			return explain(err)
		}
		if err := u.reread(ctx, w); err != nil {
			// The write went through; only the confirmation did not. Worth
			// saying, but not worth failing the action the user asked for.
			return nil
		}
	}
	return nil
}

func (u *Unit) write(ctx context.Context, w eda.Write) error {
	return u.bus.Do(ctx, bus.PriorityWrite, func(c *modbus.Client) error {
		// Function codes 15 and 16 throughout. Measurement showed 5 and 6
		// reach the unit too, now that the original adapter is out of the way,
		// but 15 and 16 are what has been proven over both paths.
		if w.Coil {
			return c.WriteCoils(w.Addr, []bool{w.Bit})
		}
		return c.WriteRegisters(w.Addr, w.Regs)
	})
}

func (u *Unit) reread(ctx context.Context, w eda.Write) error {
	if w.Coil {
		return u.poller.Reread(ctx, bus.PriorityWrite, true, int(w.Addr), 1)
	}
	return u.poller.Reread(ctx, bus.PriorityWrite, false, int(w.Addr), len(w.Regs))
}

// unstable applies the two thresholds, remembering which side of them the line
// was last on.
//
// Rising past DegradedErrorRate says so; falling below it does not take it
// back. The rate is the share of the last 256 attempts, so a line sitting near
// the mark crosses it every few minutes as failures age out — and a warning
// that comes and goes on its own is one nobody reads by the third time.
func (u *Unit) unstable(rate float64, samples int) bool {
	return u.unstableAt(time.Now(), rate, samples)
}

// unstableAt is unstable with the clock passed in.
//
// Past the mark starts a clock, and only a rate that stays past it for
// degradeAfter is called unstable; dipping below the mark stops the clock,
// because the question is whether the line has become bad, not whether it has
// had a bad minute. Recovering still takes falling below HealthyErrorRate.
func (u *Unit) unstableAt(now time.Time, rate float64, samples int) bool {
	// A rate over a handful of attempts is not a rate. A freshly started
	// daemon has made fifty-odd, and one failure among them reads as two per
	// cent — enough to raise an alarm about a line that is fine, every time
	// the service restarts. Judging waits for the window to fill.
	if samples < bus.EnoughSamples {
		return u.degraded.Load()
	}
	switch {
	case rate > eda.DegradedErrorRate:
		since := u.overSince.Load()
		if since == 0 {
			since = now.UnixNano()
			u.overSince.Store(since)
		}
		if now.Sub(time.Unix(0, since)) >= u.degradeAfter {
			u.degraded.Store(true)
		}
	case rate < eda.HealthyErrorRate:
		u.overSince.Store(0)
		u.degraded.Store(false)
	default:
		u.overSince.Store(0)
	}
	return u.degraded.Load()
}
