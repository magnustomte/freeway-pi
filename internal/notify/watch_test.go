package notify

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"freewaypi/internal/eda"
	"freewaypi/internal/i18n"
)

type fakeSource struct {
	state  *eda.State
	alarms []eda.Alarm
}

func (f *fakeSource) State() (*eda.State, error)   { return f.state, nil }
func (f *fakeSource) Alarms() ([]eda.Alarm, error) { return f.alarms, nil }

func newWatcher(t *testing.T) (*Watcher, *fakeSource, *recorder) {
	t.Helper()
	r := &recorder{}
	n := New(Options{Channels: []Channel{r}, MinSeverity: Info, UnitName: "Loft",
		Logger: slog.New(slog.DiscardHandler)})
	src := &fakeSource{state: &eda.State{Link: eda.Link{Status: eda.LinkOK}}}
	// Never ticks on its own; the tests drive check directly so they are not
	// racing a timer.
	w := &Watcher{src: src, notify: n, downAfter: time.Minute,
		stop: make(chan struct{}), done: make(chan struct{})}
	return w, src, r
}

func TestAMissedPassIsNotWorthWakingAnybody(t *testing.T) {
	// One hiccup that produces a message teaches people to ignore messages.
	w, src, r := newWatcher(t)
	src.state.Link.Status = eda.LinkDown

	w.check(context.Background())
	if len(r.all()) != 0 {
		t.Fatalf("sent %d messages the moment the link dropped", len(r.all()))
	}

	w.downSince = time.Now().Add(-2 * time.Minute)
	w.check(context.Background())
	if len(r.all()) != 1 {
		t.Fatalf("%d messages after the link stayed down, want 1", len(r.all()))
	}
	if r.all()[0].Severity != Critical {
		t.Error("an unreachable unit is not a critical event")
	}
}

func TestRecoveryIsAnnounced(t *testing.T) {
	w, src, r := newWatcher(t)
	src.state.Link.Status = eda.LinkDown
	w.downSince = time.Now().Add(-2 * time.Minute)
	w.check(context.Background())

	src.state.Link.Status = eda.LinkOK
	w.check(context.Background())

	all := r.all()
	if len(all) != 2 || !all[1].Resolved {
		t.Fatalf("messages = %+v, want the alert and an all-clear", all)
	}
}

func TestClassAAlarmsAreCriticalAndSayTheUnitStopped(t *testing.T) {
	w, src, r := newWatcher(t)
	src.alarms = []eda.Alarm{{
		Code: 3, Class: eda.ClassA, State: eda.AlarmOn,
	}}
	w.check(context.Background())

	all := r.all()
	if len(all) != 1 {
		t.Fatalf("%d messages, want 1", len(all))
	}
	if all[0].Severity != Critical {
		t.Errorf("severity = %v, want critical for a class A alarm", all[0].Severity)
	}
	want := i18n.T(i18n.Default, "alarm.stopped")
	if !contains(all[0].Message, want) {
		t.Errorf("a class A message does not say the unit stopped: %q", all[0].Message)
	}
}

func TestClassBAlarmsAreOnlyWarnings(t *testing.T) {
	w, src, r := newWatcher(t)
	src.alarms = []eda.Alarm{{
		Code: 16, Class: eda.ClassB, State: eda.AlarmOn,
	}}
	w.check(context.Background())
	if r.all()[0].Severity != Warning {
		t.Fatalf("severity = %v, want warning for a class B alarm", r.all()[0].Severity)
	}
}

func TestAnAlarmThatPersistsIsStillOneMessage(t *testing.T) {
	w, src, r := newWatcher(t)
	src.alarms = []eda.Alarm{{Code: 16, Class: eda.ClassB, State: eda.AlarmOn}}
	for i := 0; i < 20; i++ {
		w.check(context.Background())
	}
	if len(r.all()) != 1 {
		t.Fatalf("%d messages for one standing alarm, want 1", len(r.all()))
	}
}

func TestAnAlarmGoingOffIsAnnounced(t *testing.T) {
	w, src, r := newWatcher(t)
	src.alarms = []eda.Alarm{{Code: 16, Class: eda.ClassB, State: eda.AlarmOn}}
	w.check(context.Background())

	src.alarms = []eda.Alarm{{Code: 16, Class: eda.ClassB, State: eda.AlarmOff}}
	w.check(context.Background())

	all := r.all()
	if len(all) != 2 || !all[1].Resolved {
		t.Fatalf("messages = %+v, want the alarm and its all-clear", all)
	}
}

func TestTheSummerTimeChangeIsReportedOnce(t *testing.T) {
	w, src, r := newWatcher(t)
	src.state.ClockDriftIsDST = true
	for i := 0; i < 5; i++ {
		w.check(context.Background())
	}
	if len(r.all()) != 1 {
		t.Fatalf("%d messages, want 1", len(r.all()))
	}
	if !contains(r.all()[0].Message, "betjeningspanelet") {
		t.Error("the message does not say where the clock is set")
	}
}

func TestServiceIsAnnouncedWhenItFallsDue(t *testing.T) {
	w, src, r := newWatcher(t)
	src.state.Service = eda.Service{Enabled: true, IntervalDays: 180, DaysSince: 180, DaysRemaining: 0}
	w.check(context.Background())
	if len(r.all()) != 1 {
		t.Fatalf("%d messages, want 1", len(r.all()))
	}

	// And nothing while it is merely approaching.
	w2, src2, r2 := newWatcher(t)
	src2.state.Service = eda.Service{Enabled: true, IntervalDays: 180, DaysSince: 120, DaysRemaining: 60}
	w2.check(context.Background())
	if len(r2.all()) != 0 {
		t.Fatal("announced a service that is two months away")
	}
}

func TestNothingIsSaidWhenAllIsWell(t *testing.T) {
	w, _, r := newWatcher(t)
	for i := 0; i < 10; i++ {
		w.check(context.Background())
	}
	if len(r.all()) != 0 {
		t.Fatalf("%d messages with nothing wrong: %+v", len(r.all()), r.all())
	}
}
