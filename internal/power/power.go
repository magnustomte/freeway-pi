// Package power watches the Raspberry Pi's own power supply.
//
// Under-voltage is the failure this project is most likely to meet in the
// field. A Pi in a utility room is usually powered by whatever charger was in
// the drawer, and a supply that sags under load corrupts SD cards, throttles
// the processor and restarts the box at random — symptoms that look like every
// other bug before anybody thinks to blame the charger.
//
// The events are milliseconds long. Sampling a flag would miss nearly all of
// them: watching one Pi for an hour turned up eighty-one events while the live
// flag read clear every time it was looked at. So this counts what the kernel
// wrote down instead, where every one of them is recorded with a timestamp.
//
// The kernel log is out of reach from inside the daemon's sandbox —
// ProtectKernelLogs=yes, which stays — so the lines come through the root
// helper, which returns only the ones that match. Nothing else the kernel has
// said crosses that boundary.
package power

import (
	"context"
	"freewaypi/internal/i18n"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"freewaypi/internal/notify"
)

// The kernel's wording, from the Raspberry Pi voltage monitor. Matched on the
// distinctive half so a change of prefix does not silence us.
const (
	detected   = "Undervoltage detected!"
	normalised = "Voltage normalised"
)

// State is what has happened to the supply.
type State struct {
	// Watched is false when the kernel log cannot be read or reports nothing
	// about voltage, so the interface can say "not known here" rather than
	// "fine".
	Watched bool `json:"watched"`
	// Now is true when the most recent thing the kernel said was that the
	// supply had sagged, with no recovery after it.
	Now bool `json:"now"`
	// Events counts detections since the box booted, because the kernel log
	// reaches back that far and no further.
	//
	// Starting over at each restart is the behaviour to keep, not a limitation
	// to work around. Restarting is what somebody does after changing the
	// supply or the cable, and a count that carried over would go on accusing
	// a charger that had already been replaced. A problem that is still there
	// says so again within minutes; the graph keeps the long record either
	// way, because it is written to disk.
	Events int `json:"events"`
	// First and Last are when they happened.
	First time.Time `json:"first,omitempty"`
	Last  time.Time `json:"last,omitempty"`
}

// Ever reports whether the supply has ever sagged.
func (s State) Ever() bool { return s.Events > 0 }

// Source returns the kernel log lines that mention the supply, newest last.
type Source func(ctx context.Context) ([]string, error)

// Watcher keeps the count up to date.
type Watcher struct {
	log    *slog.Logger
	source Source
	notify *notify.Notifier
	unit   string
	quiet  time.Duration

	mu    sync.Mutex
	state State
	// drained is the count as of the last sample taken for the history, so a
	// reader that samples can tell whether anything happened in between.
	drained int

	cancel context.CancelFunc
	done   chan struct{}
}

// Options configures a Watcher.
type Options struct {
	Logger *slog.Logger
	// Source is where the lines come from. Required; without it nothing is
	// watched, which the interface reports rather than hides.
	Source Source
	// Interval is how often to ask. The events are counted after the fact, so
	// this is about how quickly a problem is noticed, not whether it is.
	Interval time.Duration
	// Notifier is told when the supply sags, or nil to say nothing.
	Notifier *notify.Notifier
	// Unit is what this installation is called, for the message.
	Unit string
	// Quiet is how long without an event before the condition is called over.
	//
	// Generous on purpose. A sagging supply comes and goes with whatever the
	// fan is doing, so a short one would raise and clear all evening; and
	// unlike a lost connection, there is nothing to be gained from hearing
	// about it promptly a second time.
	Quiet time.Duration
}

// Watch starts counting. Call Close to stop.
func (o Options) Watch() *Watcher {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Interval <= 0 {
		o.Interval = 30 * time.Second
	}
	if o.Quiet <= 0 {
		o.Quiet = time.Hour
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &Watcher{
		log: o.Logger, source: o.Source, notify: o.Notifier,
		unit: o.Unit, quiet: o.Quiet,
		cancel: cancel, done: make(chan struct{}),
	}
	go w.loop(ctx, o.Interval)
	return w
}

// State returns what has been seen.
func (w *Watcher) State() State {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

// Drain reports whether the supply sagged since the last time it was asked.
//
// The events last milliseconds. A reader that sampled the current state would
// read clear almost every time and record a healthy supply on a box that is
// browning out every half minute; what a ten second bucket should hold is
// whether anything happened during it, not what was true at the instant it was
// looked at.
func (w *Watcher) Drain() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	was := w.state.Events > w.drained
	w.drained = w.state.Events
	return was
}

// Close stops the watcher.
func (w *Watcher) Close() {
	w.cancel()
	<-w.done
}

func (w *Watcher) loop(ctx context.Context, every time.Duration) {
	defer close(w.done)
	if w.source == nil {
		w.log.Info("the kernel log has no reader, so the power supply is not being watched")
		return
	}
	// Once at the start, so a box that has been browning out since boot says
	// so on the first page anybody opens rather than half a minute later.
	w.sample(ctx)

	// The condition is raised on its own slower beat: the first sample counts
	// the whole backlog since boot, and saying so immediately would send word
	// of an hour-old event as though it were happening, every time the daemon
	// started.
	settled := time.Now().Add(30 * time.Second)

	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		w.sample(ctx)
		if time.Now().After(settled) {
			w.tell(ctx)
		}
	}
}

// sample asks for the lines and works out the state from all of them.
//
// Counted afresh each time rather than accumulated: the kernel log holds
// everything since boot, so recounting cannot drift, and a request that fails
// leaves the previous answer standing instead of a gap.
func (w *Watcher) sample(ctx context.Context) {
	lines, err := w.source(ctx)
	if err != nil {
		w.log.Warn("could not read the kernel log, so the power supply is not being watched", "err", err)
		return
	}

	var next State
	next.Watched = true
	for _, line := range lines {
		at, sagged, ok := parse(line)
		if !ok {
			continue
		}
		if !sagged {
			next.Now = false
			continue
		}
		next.Now = true
		next.Events++
		next.Last = at
		if next.First.IsZero() {
			next.First = at
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	// The ring buffer is finite. If it has wrapped, the oldest events are gone
	// and a fresh count would be lower than the last one — which would read as
	// the problem improving while it was in fact getting worse.
	if next.Events < w.state.Events {
		next.Events = w.state.Events
		if next.Last.Before(w.state.Last) {
			next.Last = w.state.Last
		}
		if next.First.IsZero() || w.state.First.Before(next.First) {
			next.First = w.state.First
		}
	}
	w.state = next
}

// tell raises and clears the condition.
func (w *Watcher) tell(ctx context.Context) {
	if w.notify == nil {
		return
	}
	const key = "power:undervoltage"
	s := w.State()
	switch {
	case s.Events == 0:
	case s.Now || time.Since(s.Last) < w.quiet:
		w.notify.Raise(ctx, notify.Event{
			Key: key, Kind: "undervoltage", Severity: notify.Warning,
			Title:   i18n.T(w.notify.Lang(), "notify.power.title"),
			Message: i18n.T(w.notify.Lang(), "notify.power.message", s.Events),
			Unit:    w.unit,
		})
	default:
		w.notify.Clear(ctx, key)
	}
}

// parse reads one kernel log line as dmesg prints it:
//
//	[   13.860956] hwmon hwmon2: Undervoltage detected!
//
// The number is seconds since boot, which is not a clock. It becomes one by
// subtracting from now, so a line from the ring buffer lands where it happened
// rather than when it was read.
func parse(line string) (at time.Time, sagged bool, ok bool) {
	switch {
	case strings.Contains(line, detected):
		sagged = true
	case strings.Contains(line, normalised):
		sagged = false
	default:
		return time.Time{}, false, false
	}
	return stamp(line), sagged, true
}

func stamp(line string) time.Time {
	now := time.Now()
	open := strings.IndexByte(line, '[')
	close := strings.IndexByte(line, ']')
	if open < 0 || close < open {
		return now
	}
	secs, err := strconv.ParseFloat(strings.TrimSpace(line[open+1:close]), 64)
	if err != nil {
		return now
	}
	up, err := uptime()
	if err != nil {
		return now
	}
	since := up - time.Duration(secs*float64(time.Second))
	if since < 0 {
		return now
	}
	return now.Add(-since)
}
