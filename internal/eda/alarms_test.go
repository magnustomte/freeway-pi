package eda

import (
	"testing"
	"time"

	"freewaypi/internal/bus"
)

func alarmSnapshot() []uint16 { return make([]uint16, bus.LastHolding+1) }

func writeAlarm(h []uint16, index, code, state, year, month, day, hour, minute int) {
	b := AlarmLogBase + index*AlarmLogStride
	copy(h[b:], []uint16{
		uint16(code), uint16(state), uint16(year - 2000),
		uint16(month), uint16(day), uint16(hour), uint16(minute),
	})
}

func TestReadAlarmsMatchesTheUnitsOwnLog(t *testing.T) {
	// The five newest entries as they actually read on the unit.
	h := alarmSnapshot()
	writeAlarm(h, 0, 14, 0, 2026, 6, 21, 12, 25)
	writeAlarm(h, 1, 14, 0, 2026, 5, 21, 5, 10)
	writeAlarm(h, 2, 2, 0, 2026, 2, 15, 11, 25)

	alarms, err := ReadAlarms(h)
	if err != nil {
		t.Fatal(err)
	}
	if len(alarms) != AlarmLogCount {
		t.Fatalf("%d entries, want %d", len(alarms), AlarmLogCount)
	}

	newest := alarms[0]
	if newest.Code != 14 {
		t.Errorf("newest = %d, want the service reminder", newest.Code)
	}
	// The words live in the message catalogue now; what this package promises
	// is the id they are under.
	if got := newest.NameID(); got != "alarm.14.name" {
		t.Errorf("NameID = %q", got)
	}
	if newest.Class != ClassB {
		t.Errorf("class = %q, want B", newest.Class)
	}
	if !newest.Time.Equal(time.Date(2026, 6, 21, 12, 25, 0, 0, time.Local)) {
		t.Errorf("time = %v, want 21 June 2026 12:25", newest.Time)
	}
	if newest.Active() {
		t.Error("an entry in state 0 reports itself active")
	}

	cold := alarms[2]
	if cold.Code != 2 || cold.Class != ClassB {
		t.Errorf("third entry = %d class %q, want code 2 class B", cold.Code, cold.Class)
	}
	if got := cold.HelpID(); got != "alarm.2.help" {
		t.Errorf("HelpID = %q", got)
	}
}

func TestEmptySlotsAreMarkedRatherThanInvented(t *testing.T) {
	alarms, err := ReadAlarms(alarmSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range alarms {
		if !a.Empty {
			t.Fatalf("an all-zero slot was read as an alarm: %+v", a)
		}
	}
	if len(ActiveAlarms(alarms)) != 0 {
		t.Fatal("empty slots counted as active alarms")
	}
}

func TestActiveAlarmsAreTheOnesThatAreOn(t *testing.T) {
	h := alarmSnapshot()
	writeAlarm(h, 0, 16, int(AlarmOn), 2026, 9, 19, 8, 0)
	writeAlarm(h, 1, 14, int(AlarmAcknowledged), 2026, 9, 18, 8, 0)
	writeAlarm(h, 2, 2, int(AlarmOff), 2026, 9, 17, 8, 0)

	alarms, err := ReadAlarms(h)
	if err != nil {
		t.Fatal(err)
	}
	active := ActiveAlarms(alarms)
	if len(active) != 1 || active[0].Code != 16 {
		t.Fatalf("active = %+v, want only the dirty filter", active)
	}
	if alarms[1].State.StateID() != "alarm.state.acknowledged" ||
		alarms[2].State.StateID() != "alarm.state.off" {
		t.Errorf("state ids = %q, %q", alarms[1].State.StateID(), alarms[2].State.StateID())
	}
}

func TestAnUnknownCodeIsStillReported(t *testing.T) {
	// An alarm nobody can name is still an alarm that happened, and a class
	// nobody knows is better admitted than invented.
	h := alarmSnapshot()
	writeAlarm(h, 0, 99, int(AlarmOn), 2026, 9, 19, 8, 0)

	alarms, err := ReadAlarms(h)
	if err != nil {
		t.Fatal(err)
	}
	if alarms[0].Empty {
		t.Fatal("an unknown code was dropped")
	}
	if alarms[0].Class != ClassUnknown {
		t.Errorf("class = %q, want none claimed", alarms[0].Class)
	}
	// It still has a code, which is what the interface falls back to naming
	// it by when the catalogue has no entry.
	if alarms[0].Code == 0 {
		t.Error("an unknown code lost its number as well as its name")
	}
}

func TestClassAIsReservedForWhatTheManualSaysStopsTheUnit(t *testing.T) {
	// Getting this wrong either cries wolf or stays quiet about a unit that
	// has shut itself down.
	for code, wantClass := range map[int]AlarmClass{
		3: ClassA, 4: ClassA, 6: ClassA, 8: ClassA, 9: ClassA, 12: ClassA, 13: ClassA,
		1: ClassB, 2: ClassB, 5: ClassB, 11: ClassB, 14: ClassB, 16: ClassB, 17: ClassB,
	} {
		if got := ClassOf(code); got != wantClass {
			t.Errorf("code %d is class %q, want %q", code, got, wantClass)
		}
	}
}

func TestReadAlarmsRefusesAShortSnapshot(t *testing.T) {
	if _, err := ReadAlarms(make([]uint16, 100)); err == nil {
		t.Fatal("read the alarm log from a snapshot that stops before it")
	}
}
