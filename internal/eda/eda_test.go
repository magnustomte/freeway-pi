package eda

import (
	"testing"
	"time"

	"freewaypi/internal/bus"
)

// snapshot builds a snapshot the size of the real unit's map, so an index
// mistake fails here rather than on the hardware.
func snapshot() *bus.Snapshot {
	return &bus.Snapshot{
		Started:  time.Now(),
		Finished: time.Now(),
		Holding:  make([]uint16, bus.LastHolding+1),
		Coils:    make([]bool, bus.LastCoil+1),
	}
}

// setClockRegisters fills the running clock block, which is what Decode reads.
func setClockRegisters(s *bus.Snapshot, t time.Time) {
	s.Holding[HRSeconds] = uint16(t.Second())
	s.Holding[HRMinutes] = uint16(t.Minute())
	s.Holding[HRHours] = uint16(t.Hour())
	s.Holding[HRDay] = uint16(t.Day())
	s.Holding[HRMonth] = uint16(t.Month())
	s.Holding[HRYear] = uint16(t.Year() - 2000)
	s.Holding[HRWeekday] = uint16(t.Weekday())
}

func TestDecodeReadsARealisticBaseline(t *testing.T) {
	// Numbers a unit could hold, rather than zeros, so the decoding is checked
	// against something that looks like a working unit.
	s := snapshot()
	s.Holding[HRFanActual] = 60
	s.Holding[HRFanSetpoint] = 45
	s.Holding[HROverpressureInUse] = 20
	s.Holding[HRSetpoint] = 210
	s.Holding[HRServiceInterval] = 180
	s.Holding[HRDaysSinceService] = 30
	s.Holding[HRSoftwareVersion] = 600
	s.Coils[CoilECFans] = true
	s.Coils[CoilServiceReminder] = true

	st, err := Decode(s, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if st.Setpoint != 21.0 {
		t.Errorf("setpoint = %v, want 21.0", st.Setpoint)
	}
	if st.FanActual != 60 || st.FanSetpoint != 45 {
		t.Errorf("fans = %d in effect against %d set, want 60 and 45", st.FanActual, st.FanSetpoint)
	}
	if !st.ECFans {
		t.Error("EC fans not detected")
	}
	if st.SoftwareVersion != 600 {
		t.Errorf("software version = %d, want 600", st.SoftwareVersion)
	}
	if st.Service.DaysRemaining != 150 {
		t.Errorf("days remaining = %d, want 150 (180 interval less 30 elapsed)", st.Service.DaysRemaining)
	}
	if st.Mode != ModeNormal {
		t.Errorf("mode = %q with every mode coil clear, want normal", st.Mode)
	}
}

func TestDecodeReadsTemperaturesBelowZero(t *testing.T) {
	// Outdoor temperature is the one that goes negative, and it arrives as a
	// two's complement value. Reading it unsigned gives 6513.5 degrees.
	s := snapshot()
	// Written through a variable: Go refuses to convert a negative constant to
	// uint16 at compile time, which is the same mistake the decoder must not
	// make at run time.
	var belowZero int16 = -125 // -12.5 C
	s.Holding[HRFreshAir] = uint16(belowZero)
	s.Holding[HRSupply] = 195

	st, err := Decode(s, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if st.Temperatures.FreshAir != -12.5 {
		t.Errorf("fresh air = %v, want -12.5", st.Temperatures.FreshAir)
	}
	if st.Temperatures.Supply != 19.5 {
		t.Errorf("supply = %v, want 19.5", st.Temperatures.Supply)
	}
}

func TestDecodeReadsDefrostingFromTheTopBit(t *testing.T) {
	// Bit 32768 makes a signed read negative, which is why the register is
	// read unsigned throughout.
	s := snapshot()
	s.Holding[HRState] = BitDefrosting | BitOverpressure

	st, err := Decode(s, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Defrosting {
		t.Fatal("defrosting not detected")
	}
	if len(st.Flags) != 2 {
		t.Fatalf("%d flags, want 2: %+v", len(st.Flags), st.Flags)
	}
	if !st.Overpressure {
		t.Fatal("overpressure not detected")
	}
}

// TestOverpressureComesFromTheStateNotTheCoil: the coil says what was asked
// for, and it is not the only way overpressure starts — a cooker hood switch
// does it too, and the unit ends it by itself when the minutes run out. The
// history is about what happened, so it follows the unit's own report.
func TestOverpressureComesFromTheStateNotTheCoil(t *testing.T) {
	s := snapshot()
	s.Coils[CoilOverpressure] = true
	s.Holding[HRState] = 0 // pressed, not running yet

	st, err := Decode(s, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if st.Overpressure {
		t.Error("recorded as running on a coil the unit has not acted on")
	}
	if st.Mode != ModeOverpressure {
		t.Error("the mode should follow the coil, so the button does not flicker back")
	}

	// And the other way: running without the coil, which is what a cooker hood
	// switch looks like from here.
	s = snapshot()
	s.Holding[HRState] = BitOverpressure
	st, err = Decode(s, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Overpressure {
		t.Fatal("the unit said it was running overpressure and it was not recorded")
	}
}

func TestDecodeTakesModeFromCoilsNotTheStateField(t *testing.T) {
	// The state field lags the coils by the better part of ten seconds. A mode
	// read from it flickers back to the old value right after a press, which
	// looks like the command failed.
	s := snapshot()
	s.Coils[CoilOverpressure] = true
	s.Holding[HRState] = 0 // has not caught up yet

	st, err := Decode(s, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode != ModeOverpressure {
		t.Fatalf("mode = %q, want overpressure from the coil", st.Mode)
	}
}

func TestDecodeReportsStaleAndIncompleteHonestly(t *testing.T) {
	s := snapshot()
	s.Finished = time.Now().Add(-5 * time.Minute)
	s.Failed = []bus.BlockError{{Kind: "holding", From: 0, Count: 125}}

	st, err := Decode(s, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Stale {
		t.Error("a five minute old snapshot was not reported as stale")
	}
	if !st.Incomplete {
		t.Error("a pass with a failed block was not reported as incomplete")
	}
	if st.AgeSeconds < 250 {
		t.Errorf("age = %v s, want about 300", st.AgeSeconds)
	}
}

func TestDecodeRejectsASnapshotTooSmallToTrust(t *testing.T) {
	small := &bus.Snapshot{Finished: time.Now(), Holding: make([]uint16, 10), Coils: make([]bool, 5)}
	if _, err := Decode(small, time.Minute); err == nil {
		t.Fatal("a truncated snapshot was decoded instead of refused")
	}
	if _, err := Decode(nil, time.Minute); err == nil {
		t.Fatal("a missing snapshot was decoded")
	}
}

func TestDecodeClockRejectsImpossibleDates(t *testing.T) {
	s := snapshot()
	s.Holding[HRDay], s.Holding[HRMonth], s.Holding[HRYear] = 31, 2, 26 // 31 February
	st, _ := Decode(s, time.Minute)
	if !st.UnitClock.IsZero() {
		t.Fatalf("31 February was accepted as %v", st.UnitClock)
	}

	s = snapshot()
	s.Holding[HRDay], s.Holding[HRMonth], s.Holding[HRYear] = 18, 9, 26
	s.Holding[HRHours], s.Holding[HRMinutes], s.Holding[HRSeconds] = 11, 14, 21
	st, _ = Decode(s, time.Minute)
	if st.UnitClock.IsZero() {
		t.Fatal("a valid date was rejected")
	}
	if st.UnitClock.Hour() != 11 || st.UnitClock.Day() != 18 {
		t.Fatalf("clock decoded to %v", st.UnitClock)
	}
}

func TestSetModeWritesOnlyWhatMustChange(t *testing.T) {
	// Clearing all six unrelated coils to select one would make a mode change
	// visibly slow, and the unit takes a noticeable moment per write.
	writes, err := SetMode(ModeAway, ModeOverpressure)
	if err != nil {
		t.Fatal(err)
	}
	if len(writes) != 2 {
		t.Fatalf("%d writes, want 2: %+v", len(writes), writes)
	}
	if writes[0].Addr != CoilAway || writes[0].Bit {
		t.Errorf("first write should clear the away coil, got %+v", writes[0])
	}
	if writes[1].Addr != CoilOverpressure || !writes[1].Bit {
		t.Errorf("second write should set the overpressure coil, got %+v", writes[1])
	}
}

func TestSetModeToNormalOnlyClears(t *testing.T) {
	writes, err := SetMode(ModeBoost, ModeNormal)
	if err != nil {
		t.Fatal(err)
	}
	if len(writes) != 1 || writes[0].Addr != CoilBoost || writes[0].Bit {
		t.Fatalf("want a single clear of the boost coil, got %+v", writes)
	}
}

func TestSetModeToTheCurrentModeWritesNothing(t *testing.T) {
	writes, err := SetMode(ModeAway, ModeAway)
	if err != nil {
		t.Fatal(err)
	}
	if len(writes) != 0 {
		t.Fatalf("%d writes for a mode that is already set", len(writes))
	}
}

func TestModeWritesNeverTouchTheCoilsBetweenTheModes(t *testing.T) {
	// Coils 4, 5, 8, 9 and 11 are the cooker hood, the central vacuum and the
	// bits permitting CO2, humidity and temperature boost. A block write over
	// the mode range would turn them off without anyone noticing.
	forbidden := map[uint16]string{
		4: "cooker hood", 5: "central vacuum",
		8: "CO2 boost allowed", 9: "humidity boost allowed", 11: "temperature boost allowed",
	}
	for _, from := range Modes() {
		for _, to := range Modes() {
			writes, err := SetMode(from, to)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range writes {
				if !w.Coil {
					continue
				}
				if name, bad := forbidden[w.Addr]; bad {
					t.Fatalf("%s -> %s writes coil %d, which is %s", from, to, w.Addr, name)
				}
				if len(w.Regs) != 0 {
					t.Fatalf("%s -> %s writes a coil and registers in one operation", from, to)
				}
			}
		}
	}
}

func TestSetSetpointRoundsAndRefusesTheImpossible(t *testing.T) {
	writes, err := SetSetpoint(21.25, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if writes[0].Regs[0] != 213 {
		t.Errorf("21.25 became %d, want 213; truncating would give 212", writes[0].Regs[0])
	}
	if writes[0].Addr != HRSetpoint {
		t.Errorf("wrote to %d, want %d", writes[0].Addr, HRSetpoint)
	}
	for _, bad := range []float64{9.9, 30.1, -5} {
		if _, err := SetSetpoint(bad, 0, 0); err == nil {
			t.Errorf("accepted %v °C", bad)
		}
	}
}

func TestSetSetpointRespectsTheUnitsOwnLimits(t *testing.T) {
	// A unit may allow a narrower span than the register list's 10 to 30; the
	// example here is 18 to 26.
	// Refusing what the panel would refuse beats writing a value the unit
	// quietly ignores.
	if _, err := SetSetpoint(16, 18, 26); err == nil {
		t.Error("accepted 16 °C against limits of 18 to 26")
	}
	if _, err := SetSetpoint(28, 18, 26); err == nil {
		t.Error("accepted 28 °C against limits of 18 to 26")
	}
	if _, err := SetSetpoint(22, 18, 26); err != nil {
		t.Errorf("refused 22 °C inside the limits: %v", err)
	}
	// Nonsense from the unit falls back to the documented range rather than
	// locking the interface out of every value.
	if _, err := SetSetpoint(22, 0, 0); err != nil {
		t.Errorf("unusable limits did not fall back: %v", err)
	}
	if _, err := SetSetpoint(22, 30, 10); err != nil {
		t.Errorf("reversed limits did not fall back: %v", err)
	}
}

func TestDecodeReadsTheUnitsSetpointLimits(t *testing.T) {
	s := snapshot()
	s.Holding[HRSetpointMin] = 18
	s.Holding[HRSetpointMax] = 26
	st, err := Decode(s, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if st.SetpointMin != 18 || st.SetpointMax != 26 {
		t.Fatalf("limits = %v..%v, want 18..26", st.SetpointMin, st.SetpointMax)
	}

	// Zero registers on a unit that has never had them set must not collapse
	// the slider to nothing.
	s = snapshot()
	st, _ = Decode(s, time.Minute)
	if st.SetpointMin != SetpointMin || st.SetpointMax != SetpointMax {
		t.Fatalf("limits = %v..%v with empty registers, want the documented range", st.SetpointMin, st.SetpointMax)
	}
}

func TestSetFanLevelRefusesACFansRatherThanGuessing(t *testing.T) {
	if _, err := SetFanLevel(false, 50); err == nil {
		t.Fatal("accepted a percentage for AC fans, which use steps 1 to 8")
	}
	if _, err := SetFanLevel(true, 19); err == nil {
		t.Fatal("accepted 19%, below the minimum EC fans run at")
	}
	writes, err := SetFanLevel(true, 55)
	if err != nil {
		t.Fatal(err)
	}
	if writes[0].Addr != HRFanSetpoint || writes[0].Regs[0] != 55 {
		t.Fatalf("unexpected write: %+v", writes[0])
	}
}

func TestOverpressureWritesBothCopiesInOneGo(t *testing.T) {
	// The register list calls 56 the time remaining and read only; on the unit measured
	// it is the one the unit acts on. They are adjacent, so both go in one
	// write and cannot end up disagreeing.
	writes, err := SetOverpressureDuration(30)
	if err != nil {
		t.Fatal(err)
	}
	if len(writes) != 1 {
		t.Fatalf("%d writes, want 1", len(writes))
	}
	if writes[0].Addr != HROverpressureInUse {
		t.Errorf("wrote to %d, want %d", writes[0].Addr, HROverpressureInUse)
	}
	if len(writes[0].Regs) != 2 || writes[0].Regs[0] != 30 || writes[0].Regs[1] != 30 {
		t.Errorf("registers = %v, want both copies set to 30", writes[0].Regs)
	}
}

func TestStartOverpressureSetsTheDurationBeforeTurningItOn(t *testing.T) {
	// The other order lets the unit begin a run on the previous duration.
	writes, err := StartOverpressure(ModeNormal, 45)
	if err != nil {
		t.Fatal(err)
	}
	if len(writes) != 2 {
		t.Fatalf("%d writes, want 2: %+v", len(writes), writes)
	}
	if writes[0].Coil {
		t.Fatal("the coil was written before the duration")
	}
	if !writes[1].Coil || writes[1].Addr != CoilOverpressure || !writes[1].Bit {
		t.Fatalf("second write should turn overpressure on, got %+v", writes[1])
	}
}

func TestStartBoostSetsTheDurationBeforeTurningItOn(t *testing.T) {
	// Same order and the same reason as overpressure: the other way round lets
	// the unit begin a run on the previous duration.
	writes, err := StartBoost(ModeNormal, 45)
	if err != nil {
		t.Fatal(err)
	}
	if len(writes) != 2 {
		t.Fatalf("%d writes, want 2: %+v", len(writes), writes)
	}
	// One register, unlike overpressure, whose duration lives in a mirrored
	// pair that both have to be written.
	if writes[0].Coil || writes[0].Addr != HRBoostMinutes || len(writes[0].Regs) != 1 {
		t.Fatalf("first write should be the duration alone, got %+v", writes[0])
	}
	if writes[0].Regs[0] != 45 {
		t.Errorf("duration = %d, want 45", writes[0].Regs[0])
	}
	if !writes[1].Coil || writes[1].Addr != CoilBoost || !writes[1].Bit {
		t.Fatalf("second write should turn boost on, got %+v", writes[1])
	}
}

func TestStartBoostRefusesNonsenseDurations(t *testing.T) {
	for _, minutes := range []int{0, -5, MaxTimedMinutes + 1} {
		if _, err := StartBoost(ModeNormal, minutes); err == nil {
			t.Errorf("%d minutes was accepted", minutes)
		}
	}
}

func TestSetClockWritesTheSettingBlockAtOnce(t *testing.T) {
	// Five adjacent registers in one write, so the clock cannot be left half
	// set with the date changed and the time not.
	writes := SetClock(Time{Second: 21, Minute: 14, Hour: 13, Day: 18, Month: 9, Year: 2026, Weekday: 5})
	if len(writes) != 1 {
		t.Fatalf("%d writes, want 1", len(writes))
	}
	// Not the running clock at HR37: that one takes a write, reads it back,
	// and is overwritten by the unit a second later. This is the block that
	// actually moves the clock.
	if writes[0].Addr != HRSetMinute {
		t.Errorf("wrote to %d, want %d", writes[0].Addr, HRSetMinute)
	}
	want := []uint16{14, 13, 18, 9, 26}
	if len(writes[0].Regs) != len(want) {
		t.Fatalf("registers = %v, want %v", writes[0].Regs, want)
	}
	for i := range want {
		if writes[0].Regs[i] != want[i] {
			t.Fatalf("registers = %v, want %v", writes[0].Regs, want)
		}
	}
}

// TestSetClockDropsSecondsAndWeekday: the setting block has no register for
// either, and quietly sending them would write the seconds into the hour.
func TestSetClockDropsSecondsAndWeekday(t *testing.T) {
	writes := SetClock(Time{Second: 59, Minute: 0, Hour: 7, Day: 1, Month: 1, Year: 2027, Weekday: 6})
	got := writes[0].Regs
	if len(got) != 5 {
		t.Fatalf("%d registers, want 5: %v", len(got), got)
	}
	if got[0] != 0 || got[1] != 7 {
		t.Errorf("minute and hour = %d, %d; want 0, 7 — seconds have leaked in", got[0], got[1])
	}
}

func TestEveryWriteCarriesADescriptionForTheAuditLog(t *testing.T) {
	var all []Write
	m, _ := SetMode(ModeNormal, ModeAway)
	sp, _ := SetSetpoint(21, 0, 0)
	fan, _ := SetFanLevel(true, 40)
	op, _ := SetOverpressureDuration(20)
	all = append(all, m...)
	all = append(all, sp...)
	all = append(all, fan...)
	all = append(all, op...)
	all = append(all, SetHeatingAllowed(true)...)
	all = append(all, SetCoolingAllowed(false)...)
	all = append(all, SetClock(Time{Year: 2026, Month: 9, Day: 18})...)

	for _, w := range all {
		if w.Describe == "" {
			t.Errorf("a write to %d has no description: %+v", w.Addr, w)
		}
	}
}

func TestAnHourOutIsRecognisedAsTheSummerTimeChange(t *testing.T) {
	// The clock is set by hand on the panel and has to be reset twice a year.
	// An hour is not where a drifting clock lands, so saying what happened
	// beats reporting a mysterious hour.
	cases := []struct {
		name  string
		drift time.Duration
		want  bool
	}{
		{"exactly an hour", time.Hour, true},
		{"an hour and a couple of minutes", time.Hour + 2*time.Minute, true},
		{"an hour less a couple of minutes", time.Hour - 2*time.Minute, true},
		{"half an hour", 30 * time.Minute, false},
		{"two hours", 2 * time.Hour, false},
		{"a minute", time.Minute, false},
	}
	for _, c := range cases {
		s := snapshot()
		when := time.Now().Add(-c.drift)
		s.Holding[HRSeconds] = uint16(when.Second())
		s.Holding[HRMinutes] = uint16(when.Minute())
		s.Holding[HRHours] = uint16(when.Hour())
		s.Holding[HRDay] = uint16(when.Day())
		s.Holding[HRMonth] = uint16(when.Month())
		s.Holding[HRYear] = uint16(when.Year() - 2000)

		st, err := Decode(s, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if st.ClockDriftIsDST != c.want {
			t.Errorf("%s: reported as summer time = %v, want %v (drift %v)",
				c.name, st.ClockDriftIsDST, c.want, st.UnitClockDrift.Round(time.Second))
		}
	}
}

// TestTheLadderInHR49: negative is cooling, 0 to 99 is heat recovery, and 100
// and above is recovery at full plus heating by the remainder.
//
// Measured, not assumed, and the measurement is worth keeping. In two
// consecutive readings HR49 went from 101 to 102 and HR45 changed from 2
// (recovery) to 4 (heating) at the same moment, with the supply air rising and
// the extract steady. The threshold is real and it is at 100.
//
// This file briefly claimed the opposite, on the strength of two days of
// history showing the value never exceeded 4. That history was HR45 — the
// stored metric is called control_step and reads register 45, not 49 — so one
// register was condemned on another's data. The lesson is in the name of the
// series, not in the numbers.
func TestTheLadderInHR49(t *testing.T) {
	for _, c := range []struct {
		ladder int16
		want   Output
	}{
		{-15, Output{Cooling: 15}},
		{-100, Output{Cooling: 100}},
		{0, Output{}},
		{63, Output{Recovery: 63}},
		{99, Output{Recovery: 99}},
		{101, Output{Recovery: 100, Heating: 1}}, // measured, still recovery
		{102, Output{Recovery: 100, Heating: 2}}, // measured, heating begins
		{145, Output{Recovery: 100, Heating: 45}},
		// Out of range either way is clamped rather than shown as a percentage
		// nobody can act on.
		{-130, Output{Cooling: 100}},
		{260, Output{Recovery: 100, Heating: 100}},
	} {
		got := readOutput(c.ladder, 0)
		got.State, got.Step = "", 0 // the step is the other half; checked below
		if got != c.want {
			t.Errorf("readOutput(%d) = %+v, want %+v", c.ladder, got, c.want)
		}
	}
}

// TestOutputIsNeverBothWays: the unit heats or cools, never both, and an
// interface that showed both would be reporting something impossible.
func TestOutputIsNeverBothWays(t *testing.T) {
	for ladder := -300; ladder <= 300; ladder++ {
		o := readOutput(int16(ladder), 0)
		if o.Cooling > 0 && (o.Heating > 0 || o.Recovery > 0) {
			t.Fatalf("HR49=%d gives cooling and heating at once: %+v", ladder, o)
		}
		if o.Heating > 0 && o.Recovery != 100 {
			t.Fatalf("HR49=%d heats without recovery at full: %+v", ladder, o)
		}
	}
}

// TestTheStepNamesWhatTheLadderCannot: starting up, stopped, defrosting and
// cleaning the exchanger are not a proportion of anything, so HR45 carries
// them and HR49 says nothing about them.
func TestTheStepNamesWhatTheLadderCannot(t *testing.T) {
	for step, want := range map[int]string{
		0: OutputNone, 1: OutputCooling, 2: OutputRecovery, 4: OutputHeating,
		5: OutputStepDelay, 6: OutputSummerNC, 7: OutputStartup,
		8: OutputStopped, 9: OutputHRClean, 10: OutputDefrost,
	} {
		if got := readOutput(0, step); got.State != want {
			t.Errorf("step %d = %q, want %q", step, got.State, want)
		}
	}
	// A step the register list does not name is admitted rather than invented:
	// calling an unknown step "nothing" would say the unit is idle when it is
	// not, and the raw value is kept so it can still be reported.
	for _, step := range []int{3, 11, 99} {
		got := readOutput(0, step)
		if got.State != "" || got.Step != step {
			t.Errorf("step %d = %+v, want no name and the number kept", step, got)
		}
	}
}

// TestTheMeasuredCrossing: the two registers agreeing is what settled this, so
// the pair is pinned together rather than each on its own.
func TestTheMeasuredCrossing(t *testing.T) {
	below := readOutput(101, 2)
	if below.Recovery != 100 || below.Heating != 1 || below.State != OutputRecovery {
		t.Errorf("at the reading before the crossing: %+v", below)
	}
	above := readOutput(102, 4)
	if above.Recovery != 100 || above.Heating != 2 || above.State != OutputHeating {
		t.Errorf("at the reading after the crossing: %+v", above)
	}
}

// TestClockDriftIsMeasuredAtTheReading: the unit's clock is compared with the
// instant the registers were read, not with the present. A snapshot is up to a
// poll interval old, and using the present adds that whole interval to the
// drift — which made a unit synchronised to the second report eight seconds
// out, and would have failed the check that a clock write had worked.
func TestClockDriftIsMeasuredAtTheReading(t *testing.T) {
	read := time.Date(2026, 9, 19, 14, 1, 2, 0, time.Local)
	snap := snapshot()
	// A pass that began at the reading and took a second and a half. The clock
	// registers are in the first block, so the reading is Started.
	snap.Started, snap.Finished = read, read.Add(1500*time.Millisecond)
	// The unit's clock agrees exactly with the moment of the reading.
	setClockRegisters(snap, read)

	st, err := Decode(snap, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if st.UnitClockDrift > time.Second {
		t.Errorf("drift = %s for a clock that matched the reading exactly", st.UnitClockDrift)
	}
}

// TestClockDriftIsReal: a unit that really is out still reports it.
func TestClockDriftIsReal(t *testing.T) {
	read := time.Date(2026, 9, 19, 14, 1, 2, 0, time.Local)
	snap := snapshot()
	snap.Started, snap.Finished = read, read.Add(1500*time.Millisecond)
	setClockRegisters(snap, read.Add(-90*time.Second))

	st, err := Decode(snap, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if d := st.UnitClockDrift; d < 89*time.Second || d > 91*time.Second {
		t.Errorf("drift = %s, want about 90s", d)
	}
}

// TestReadMachine covers every model in the adapter's list, and what happens
// to a unit that is not in it.
//
// The list is Enervent's own, taken from the original Freeway WEB interface.
// The box has to be able to say what any of them is, and to answer honestly for
// one it has never heard of rather than claiming to be the first in the list.
func TestReadMachine(t *testing.T) {
	for code, want := range map[int]string{
		0: "Pingvin", 1: "Pandion", 2: "Pelican", 3: "Pegasos", 4: "Pegasos XL",
		5: "LTR-3", 6: "LTR-6", 7: "LTR-7", 8: "LTR-7-XL",
	} {
		s := snapshot()
		s.Holding[HRMachineFamily] = uint16(code)
		if got := readMachine(s.Holding); got.Family != want {
			t.Errorf("code %d = %q, want %q", code, got.Family, want)
		}
	}

	// Past the end of the list: no name, but the code is kept so the interface
	// can say what it actually answered.
	s := snapshot()
	s.Holding[HRMachineFamily] = 42
	got := readMachine(s.Holding)
	if got.Family != "" {
		t.Errorf("code 42 = %q, want no name at all", got.Family)
	}
	if got.FamilyCode != 42 {
		t.Errorf("FamilyCode = %d, want the 42 it answered", got.FamilyCode)
	}
}

// TestReadMachineSerial: zero means never written, which is not rare, and the
// interface hides it rather than showing a serial of nought.
func TestReadMachineSerial(t *testing.T) {
	s := snapshot()
	s.Holding[HRSerialNumber] = 0
	if got := readMachine(s.Holding); got.Serial != 0 {
		t.Errorf("Serial = %d, want 0", got.Serial)
	}
	s.Holding[HRSerialNumber] = 12345
	if got := readMachine(s.Holding); got.Serial != 12345 {
		t.Errorf("Serial = %d, want 12345", got.Serial)
	}
}

// TestTimedDurationsHonourTheDocumentedRange.
//
// The register list gives 0 to 60 minutes for both overpressure and boost, and
// the unit enforces neither — measured, it accepted 90 in each and held it. So
// this is the only place the limit exists, and a test is the only thing that
// keeps it from drifting back to whatever number looked generous.
func TestTimedDurationsHonourTheDocumentedRange(t *testing.T) {
	for _, c := range []struct {
		name string
		fn   func(int) ([]Write, error)
	}{
		{"overpressure", SetOverpressureDuration},
		{"boost", SetBoostDuration},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.fn(MaxTimedMinutes); err != nil {
				t.Errorf("%d minutes was refused: %v", MaxTimedMinutes, err)
			}
			if _, err := c.fn(MaxTimedMinutes + 1); err == nil {
				t.Errorf("%d minutes was accepted", MaxTimedMinutes+1)
			}
			if _, err := c.fn(0); err == nil {
				t.Error("zero was accepted; starting a timed mode for no time is not a request")
			}
		})
	}
	if MaxTimedMinutes != 60 {
		t.Errorf("MaxTimedMinutes = %d; the register list says 60", MaxTimedMinutes)
	}
}
