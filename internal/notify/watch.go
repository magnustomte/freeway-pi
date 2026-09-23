package notify

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"freewaypi/internal/eda"
	"freewaypi/internal/i18n"
)

// Source is what the watcher looks at.
type Source interface {
	State() (*eda.State, error)
	Alarms() ([]eda.Alarm, error)
}

// Watcher turns the unit's state into events.
//
// It raises and clears rather than sending: everything about not repeating
// itself lives in the Notifier, so this only has to decide what is true now.
type Watcher struct {
	src    Source
	notify *Notifier
	// downAfter is how long the unit may be unreachable before it is worth
	// waking somebody. A single missed pass is a hiccup.
	downAfter time.Duration

	stop chan struct{}
	done chan struct{}
	once sync.Once

	// downSince is when the link was first seen down, so the alert waits.
	downSince time.Time
}

// WatcherOptions configures a Watcher.
type WatcherOptions struct {
	Interval  time.Duration
	DownAfter time.Duration
}

// alarmName is the alarm's name, or a placeholder naming the code for one the
// manual's table never covered.
func (w *Watcher) alarmName(a eda.Alarm) string {
	if i18n.Has(w.notify.Lang(), a.NameID()) {
		return i18n.T(w.notify.Lang(), a.NameID())
	}
	return i18n.T(w.notify.Lang(), "alarm.unknown", a.Code)
}

// Watch starts watching. Call Close to stop.
func Watch(src Source, n *Notifier, o WatcherOptions) *Watcher {
	if o.Interval <= 0 {
		o.Interval = 30 * time.Second
	}
	if o.DownAfter <= 0 {
		o.DownAfter = 5 * time.Minute
	}
	// Messages are written in the notifier's language, asked for each time
	// rather than fixed here, so saving the settings in another language
	// changes what the next message says without a restart.
	w := &Watcher{
		src: src, notify: n, downAfter: o.DownAfter,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	go w.loop(o.Interval)
	return w
}

func (w *Watcher) loop(interval time.Duration) {
	defer close(w.done)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
			w.check(context.Background())
		}
	}
}

// check compares what is true now against what has been reported.
func (w *Watcher) check(ctx context.Context) {
	st, err := w.src.State()
	if err != nil {
		return // nothing read yet; not an event in itself
	}

	w.checkLink(ctx, st)
	w.checkClock(ctx, st)
	w.checkService(ctx, st)
	w.checkAlarms(ctx)
}

func (w *Watcher) checkLink(ctx context.Context, st *eda.State) {
	switch st.Link.Status {
	case eda.LinkDown:
		// Waited out, because one missed pass is a hiccup and a message about
		// it teaches people to ignore messages.
		if w.downSince.IsZero() {
			w.downSince = time.Now()
			return
		}
		if time.Since(w.downSince) < w.downAfter {
			return
		}
		w.notify.Raise(ctx, Event{
			Key: "link:down", Kind: "link", Severity: Critical,
			Title:   i18n.T(w.notify.Lang(), "notify.link.down.title"),
			Message: i18n.T(w.notify.Lang(), "notify.link.down.message", time.Since(w.downSince).Round(time.Minute)),
		})
	case eda.LinkDegraded:
		w.downSince = time.Time{}
		w.notify.Clear(ctx, "link:down")
		w.notify.Raise(ctx, Event{
			Key: "link:degraded", Kind: "link", Severity: Warning,
			Title: i18n.T(w.notify.Lang(), "notify.link.degraded.title"),
			// Detail is a catalogue id, which the page looks up and this used
			// to send down the webhook as it was.
			Message: linkDetail(w.notify.Lang(), st.Link),
		})
	default:
		w.downSince = time.Time{}
		w.notify.Clear(ctx, "link:down")
		w.notify.Clear(ctx, "link:degraded")
	}
}

func (w *Watcher) checkClock(ctx context.Context, st *eda.State) {
	if st.ClockDriftIsDST {
		w.notify.Raise(ctx, Event{
			Key: "clock:dst", Kind: "clock", Severity: Warning,
			Title:   i18n.T(w.notify.Lang(), "notify.clock.dst.title"),
			Message: i18n.T(w.notify.Lang(), "notify.clock.dst.message"),
		})
		return
	}
	w.notify.Clear(ctx, "clock:dst")
}

func (w *Watcher) checkService(ctx context.Context, st *eda.State) {
	if st.Service.Enabled && st.Service.IntervalDays > 0 && st.Service.DaysRemaining == 0 {
		w.notify.Raise(ctx, Event{
			Key: "service:due", Kind: "service", Severity: Warning,
			Title:   i18n.T(w.notify.Lang(), "notify.service.title"),
			Message: i18n.T(w.notify.Lang(), "notify.service.message", st.Service.DaysSince),
		})
		return
	}
	w.notify.Clear(ctx, "service:due")
}

func (w *Watcher) checkAlarms(ctx context.Context) {
	alarms, err := w.src.Alarms()
	if err != nil {
		return
	}

	active := map[int]eda.Alarm{}
	for _, a := range eda.ActiveAlarms(alarms) {
		active[a.Code] = a
	}
	for code, a := range active {
		// Class A stops the unit; class B does not. Getting that wrong either
		// cries wolf or stays quiet about a house with no ventilation.
		severity := Warning
		if a.Class == eda.ClassA {
			severity = Critical
		}
		var message string
		if i18n.Has(w.notify.Lang(), a.HelpID()) {
			message = i18n.T(w.notify.Lang(), a.HelpID())
		}
		if a.Class == eda.ClassA {
			message = strings.TrimSpace(i18n.T(w.notify.Lang(), "alarm.stopped") + " " + message)
		}
		w.notify.Raise(ctx, Event{
			Key: fmt.Sprintf("alarm:%d", code), Kind: "alarm", Severity: severity,
			Title:   w.alarmName(a),
			Message: message,
		})
	}
	// Anything raised that is no longer on is over.
	for _, e := range w.notify.Active() {
		if e.Kind != "alarm" {
			continue
		}
		var code int
		if _, err := fmt.Sscanf(e.Key, "alarm:%d", &code); err != nil {
			continue
		}
		if _, still := active[code]; !still {
			w.notify.Clear(ctx, e.Key)
		}
	}
}

// Close stops watching.
func (w *Watcher) Close() {
	w.once.Do(func() { close(w.stop) })
	<-w.done
}

// linkDetail writes the link's detail in a language. The detail is a catalogue
// id, with the error rate as its argument when it has one.
func linkDetail(l i18n.Lang, link eda.Link) string {
	if link.Detail == "" {
		return ""
	}
	if link.DetailArg != 0 {
		return i18n.T(l, link.Detail, link.DetailArg)
	}
	return i18n.T(l, link.Detail)
}
