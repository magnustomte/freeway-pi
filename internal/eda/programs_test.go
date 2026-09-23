package eda

import (
	"strings"
	"testing"

	"freewaypi/internal/bus"
)

func programSnapshot() []uint16 { return make([]uint16, bus.LastHolding+1) }

func TestReadWeekProgramsMatchesWhatTheUnitHeld(t *testing.T) {
	// Two programs with the same times, split weekdays and weekend, with no
	// function chosen — the shape that settled which bit is which day.
	h := programSnapshot()
	copy(h[WeekProgramBase:], []uint16{62, 23, 0, 6, 15, 0})
	copy(h[WeekProgramBase+WeekProgramStride:], []uint16{65, 23, 0, 6, 15, 0})

	programs, err := ReadWeekPrograms(h)
	if err != nil {
		t.Fatal(err)
	}
	if len(programs) != WeekProgramCount {
		t.Fatalf("%d programs, want %d", len(programs), WeekProgramCount)
	}

	weekdays := programs[0]
	if weekdays.Days != Monday|Tuesday|Wednesday|Thursday|Friday {
		t.Errorf("mask 62 decoded to %d, want the five weekdays", weekdays.Days)
	}
	if weekdays.StartHour != 23 || weekdays.StopHour != 6 || weekdays.StopMin != 15 {
		t.Errorf("times = %02d:%02d–%02d:%02d", weekdays.StartHour, weekdays.StartMin, weekdays.StopHour, weekdays.StopMin)
	}
	if weekdays.Active() {
		t.Error("a program with no function chosen reports itself active")
	}

	weekend := programs[1]
	if weekend.Days != Sunday|Saturday {
		t.Errorf("mask 65 decoded to %d, want Sunday and Saturday", weekend.Days)
	}
}

func TestDayBitsPutSundayFirst(t *testing.T) {
	// The register list contradicts itself on this. Measurement settled it from
	// the unit's own data, and holding register 43 agrees: Sunday is 0.
	if Sunday != 1 {
		t.Errorf("Sunday = %d, want bit 0", Sunday)
	}
	if Monday|Tuesday|Wednesday|Thursday|Friday != 62 {
		t.Errorf("the weekdays make %d, want 62 as found on the unit", Monday|Tuesday|Wednesday|Thursday|Friday)
	}
	if Sunday|Saturday != 65 {
		t.Errorf("the weekend makes %d, want 65 as found on the unit", Sunday|Saturday)
	}
	if AllDays != Sunday|Monday|Tuesday|Wednesday|Thursday|Friday|Saturday {
		t.Error("AllDays does not cover every day")
	}
}

func TestWriteWeekProgramIsOneOperation(t *testing.T) {
	// Six adjacent registers in one write, so a start time can never be
	// applied without its stop time and run until somebody notices.
	writes, err := WriteWeekProgram(WeekProgram{
		Index: 3, Days: Monday | Friday,
		StartHour: 6, StartMin: 30, StopHour: 8, StopMin: 0,
		Function: FunctionTemperatureDrop,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(writes) != 1 {
		t.Fatalf("%d writes, want 1", len(writes))
	}
	w := writes[0]
	if w.Addr != WeekProgramBase+3*WeekProgramStride {
		t.Errorf("address %d, want %d", w.Addr, WeekProgramBase+3*WeekProgramStride)
	}
	want := []uint16{Monday | Friday, 6, 30, 8, 0, uint16(FunctionTemperatureDrop)}
	if len(w.Regs) != len(want) {
		t.Fatalf("%d registers, want %d", len(w.Regs), len(want))
	}
	for i := range want {
		if w.Regs[i] != want[i] {
			t.Fatalf("registers = %v, want %v", w.Regs, want)
		}
	}
	if !strings.Contains(w.Describe, "06:30") {
		t.Errorf("the audit line does not read as a time: %q", w.Describe)
	}
}

func TestWriteWeekProgramRefusesNonsense(t *testing.T) {
	base := WeekProgram{Index: 0, Days: AllDays, Function: FunctionNone}
	cases := map[string]WeekProgram{
		"no such program":  {Index: WeekProgramCount, Days: AllDays},
		"hour past 23":     {Index: 0, StartHour: 24},
		"minute past 59":   {Index: 0, StopMin: 60},
		"negative hour":    {Index: 0, StopHour: -1},
		"impossible days":  {Index: 0, Days: 200},
		"unknown function": {Index: 0, Function: 17},
	}
	for name, p := range cases {
		if _, err := WriteWeekProgram(p); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := WriteWeekProgram(base); err != nil {
		t.Errorf("a valid program was refused: %v", err)
	}
}

func TestFanSpeedIsAFunctionValueInItsOwnRight(t *testing.T) {
	// On EC fans the function value is the percentage, which is why 8 to 15,
	// the AC fan steps, are not offered.
	f, err := FanFunction(45)
	if err != nil {
		t.Fatal(err)
	}
	if int(f) != 45 || !IsFanSpeed(f) {
		t.Fatalf("45 %% became %d", int(f))
	}
	if _, err := FanFunction(15); err == nil {
		t.Error("accepted 15 %, below what EC fans run at")
	}
	if _, err := FanFunction(120); err == nil {
		t.Error("accepted 120 %")
	}
	if got := FunctionName(f); !strings.Contains(got, "45") {
		t.Errorf("name = %q, should mention the percentage", got)
	}
	// A program set to a fan speed must survive a round trip.
	writes, err := WriteWeekProgram(WeekProgram{Index: 0, Days: AllDays, Function: f})
	if err != nil {
		t.Fatal(err)
	}
	if writes[0].Regs[5] != 45 {
		t.Fatalf("stored function = %d, want 45", writes[0].Regs[5])
	}
}

func TestYearProgramsRoundTrip(t *testing.T) {
	p := YearProgram{
		Index:    2,
		StartDay: 15, StartMonth: 12, StartYear: 2026, StartHour: 18, StartMin: 0,
		StopDay: 5, StopMonth: 1, StopYear: 2027, StopHour: 12, StopMin: 30,
		Function: FunctionLongAway,
	}
	writes, err := WriteYearProgram(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(writes) != 1 || len(writes[0].Regs) != YearProgramStride {
		t.Fatalf("want one write of %d registers, got %d writes", YearProgramStride, len(writes))
	}

	h := programSnapshot()
	copy(h[writes[0].Addr:], writes[0].Regs)
	back, err := ReadYearPrograms(h)
	if err != nil {
		t.Fatal(err)
	}
	got := back[2]
	if got.StartYear != 2026 || got.StopYear != 2027 {
		t.Errorf("years = %d..%d, want 2026..2027", got.StartYear, got.StopYear)
	}
	if got.StartDay != 15 || got.StopMonth != 1 || got.StopMin != 30 {
		t.Errorf("round trip lost something: %+v", got)
	}
	if got.Function != FunctionLongAway {
		t.Errorf("function = %d, want %d", got.Function, FunctionLongAway)
	}
	if !got.Active() {
		t.Error("a dated program with a function reports itself inactive")
	}
}

func TestYearProgramRefusesImpossibleDates(t *testing.T) {
	ok := YearProgram{
		StartDay: 1, StartMonth: 1, StartYear: 2027, StopDay: 2, StopMonth: 1, StopYear: 2027,
		Function: FunctionAway,
	}
	if _, err := WriteYearProgram(ok); err != nil {
		t.Fatalf("a valid program was refused: %v", err)
	}

	bad := ok
	bad.StartMonth = 13
	if _, err := WriteYearProgram(bad); err == nil {
		t.Error("accepted month 13")
	}
	bad = ok
	bad.StopDay = 0
	if _, err := WriteYearProgram(bad); err == nil {
		t.Error("accepted day 0")
	}
	bad = ok
	// The register holds the year less 2000, so 1999 would wrap round.
	bad.StartYear = 1999
	if _, err := WriteYearProgram(bad); err == nil {
		t.Error("accepted a year the register cannot hold")
	}
}

func TestReadingRefusesASnapshotThatDoesNotReachTheProgrammes(t *testing.T) {
	short := make([]uint16, 100)
	if _, err := ReadWeekPrograms(short); err == nil {
		t.Error("read weekly programs from a snapshot that stops before them")
	}
	if _, err := ReadYearPrograms(short); err == nil {
		t.Error("read yearly programs from a snapshot that stops before them")
	}
}

func TestClearingAProgramBlanksEveryRegisterOfIt(t *testing.T) {
	// A half-cleared program would keep its days and lose its function, or the
	// other way round, and read as something nobody configured.
	writes, err := ClearWeekProgram(4)
	if err != nil {
		t.Fatal(err)
	}
	if len(writes) != 1 {
		t.Fatalf("%d writes, want 1", len(writes))
	}
	if writes[0].Addr != WeekProgramBase+4*WeekProgramStride {
		t.Errorf("address %d, want %d", writes[0].Addr, WeekProgramBase+4*WeekProgramStride)
	}
	if len(writes[0].Regs) != WeekProgramStride {
		t.Fatalf("%d registers, want all %d", len(writes[0].Regs), WeekProgramStride)
	}
	for i, v := range writes[0].Regs {
		if v != 0 {
			t.Errorf("register %d = %d, want 0", i, v)
		}
	}

	// And a cleared program reads back as free.
	h := programSnapshot()
	copy(h[WeekProgramBase+4*WeekProgramStride:], []uint16{62, 22, 0, 7, 30, 5})
	copy(h[writes[0].Addr:], writes[0].Regs)
	back, err := ReadWeekPrograms(h)
	if err != nil {
		t.Fatal(err)
	}
	if back[4].Active() || back[4].Days != 0 || back[4].Function != FunctionNone {
		t.Fatalf("a cleared slot still reads as configured: %+v", back[4])
	}
}

func TestClearingTheYearProgramsToo(t *testing.T) {
	writes, err := ClearYearProgram(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(writes[0].Regs) != YearProgramStride {
		t.Fatalf("%d registers, want all %d", len(writes[0].Regs), YearProgramStride)
	}
	for _, v := range writes[0].Regs {
		if v != 0 {
			t.Fatal("a register was left set")
		}
	}
}

func TestClearingRefusesASlotThatDoesNotExist(t *testing.T) {
	if _, err := ClearWeekProgram(WeekProgramCount); err == nil {
		t.Error("cleared a weekly slot beyond the twenty")
	}
	if _, err := ClearYearProgram(-1); err == nil {
		t.Error("cleared a negative yearly slot")
	}
}
