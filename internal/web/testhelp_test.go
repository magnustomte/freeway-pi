package web

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"freewaypi/internal/auth"
	"freewaypi/internal/eda"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// stubController stands in for the unit. The clock drift is settable because
// it is what the timer program guard turns on, and applied records the writes
// so a test can assert that a refused edit really did not reach the unit.
type stubController struct {
	name         string
	drift        time.Duration
	weekdayWrong bool
	applied      [][]eda.Write
}

func (c *stubController) State() (*eda.State, error) {
	clock := time.Now().Add(-c.drift)
	expected := int(clock.Weekday())
	weekday := expected
	if c.weekdayWrong {
		weekday = (expected + 5) % 7
	}
	return &eda.State{
		Name:                "test",
		UnitClock:           clock,
		UnitClockDrift:      c.drift,
		DriftSeconds:        c.drift.Seconds(),
		UnitWeekday:         weekday,
		UnitWeekdayExpected: expected,
		WeekdayOK:           weekday == expected,
		SetpointMin:         18,
		SetpointMax:         26,
	}, nil
}

func (c *stubController) Apply(_ context.Context, w []eda.Write) error {
	c.applied = append(c.applied, w)
	return nil
}

func (c *stubController) SyncClock(context.Context) error { return nil }

func (c *stubController) SetName(name string) { c.name = name }

func (c *stubController) Settings() ([]eda.SettingValue, error) {
	return []eda.SettingValue{{Setting: eda.Settings[0], Number: 18}}, nil
}

func (c *stubController) Programs() ([]eda.WeekProgram, []eda.YearProgram, error) {
	return make([]eda.WeekProgram, eda.WeekProgramCount), make([]eda.YearProgram, eda.YearProgramCount), nil
}

func (c *stubController) Alarms() ([]eda.Alarm, error) {
	return []eda.Alarm{
		{Index: 0, Code: 14, Class: eda.ClassB, State: eda.AlarmOff},
	}, nil
}

func newAuthServer(t *testing.T) (*Server, *auth.Authenticator) {
	t.Helper()
	s, a, _ := newServerWithClock(t, 0)
	return s, a
}

func newServerWithClock(t *testing.T, drift time.Duration) (*Server, *auth.Authenticator, *stubController) {
	t.Helper()
	a := auth.New(auth.Config{})
	c := &stubController{drift: drift}
	return New(c, nil, Options{Auth: a, Logger: discardLogger()}), a, c
}
