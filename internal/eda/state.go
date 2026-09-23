package eda

import (
	"fmt"
	"time"

	"freewaypi/internal/bus"
)

// Mode is the operating mode a person chooses. The unit keeps these as coils
// that are mutually exclusive in practice, so choosing one clears the others.
type Mode string

const (
	ModeNormal       Mode = "normal"
	ModeStop         Mode = "stop"
	ModeAway         Mode = "away"
	ModeLongAway     Mode = "long_away"
	ModeOverpressure Mode = "overpressure"
	ModeMaxHeating   Mode = "max_heating"
	ModeMaxCooling   Mode = "max_cooling"
	ModeBoost        Mode = "boost"
)

// modeCoils maps each mode to the coil that selects it. Normal has none: it is
// what remains when every other coil is clear.
var modeCoils = map[Mode]uint16{
	ModeStop:         CoilStop,
	ModeAway:         CoilAway,
	ModeLongAway:     CoilLongAway,
	ModeOverpressure: CoilOverpressure,
	ModeMaxHeating:   CoilMaxHeating,
	ModeMaxCooling:   CoilMaxCooling,
	ModeBoost:        CoilBoost,
}

// Modes lists the selectable modes in the order an interface should offer them.
func Modes() []Mode {
	return []Mode{ModeNormal, ModeAway, ModeLongAway, ModeOverpressure, ModeBoost, ModeMaxHeating, ModeMaxCooling, ModeStop}
}

// Flag is one bit of holding register 44 that is currently set.
type Flag struct {
	Bit   uint16 `json:"bit"`
	Name  string `json:"name"`
	Label string `json:"label"`
}

var flagNames = []struct {
	bit   uint16
	name  string
	label string
}{
	{BitMaxCooling, "max_cooling", "flag.max_cooling"},
	{BitMaxHeating, "max_heating", "flag.max_heating"},
	{BitEmergencyStop, "emergency_stop", "flag.emergency_stop"},
	{BitStop, "stop", "flag.stop"},
	{BitAway, "away", "flag.away"},
	{BitLongAway, "long_away", "flag.long_away"},
	{BitTemperatureBoost, "temperature_boost", "flag.temperature_boost"},
	{BitCO2Boost, "co2_boost", "flag.co2_boost"},
	{BitHumidityBoost, "humidity_boost", "flag.humidity_boost"},
	{BitBoost, "boost", "flag.boost"},
	{BitOverpressure, "overpressure", "flag.overpressure"},
	{BitCookerHood, "cooker_hood", "flag.cooker_hood"},
	{BitCentralVacuum, "central_vacuum", "flag.central_vacuum"},
	{BitELHCooling, "elh_cooling", "flag.elh_cooling"},
	{BitSummerNightCool, "summer_night_cooling", "flag.summer_night_cooling"},
	{BitDefrosting, "defrosting", "flag.defrosting"},
}

// Temperatures in degrees, as the interface shows them.
type Temperatures struct {
	FreshAir            float64 `json:"fresh_air"`             // X1, outdoors
	SupplyAfterRecovery float64 `json:"supply_after_recovery"` // X2
	Supply              float64 `json:"supply"`                // X3, into the rooms
	Waste               float64 `json:"waste"`                 // X4, thrown out
	Extract             float64 `json:"extract"`               // X5, out of the rooms
	// Panel is the sensor in the control panel, and RoomMean the mean room
	// temperature the unit works from. On a house whose only room sensor is
	// the panel, they read the same.
	Panel    float64 `json:"panel"`     // T_OP1
	RoomMean float64 `json:"room_mean"` // MEAN_TEMP
	// SupplyTarget is what the supply air is being aimed at, and SupplyMin and
	// SupplyMax the range it is allowed. Not a measurement — a decision, and
	// the one that explains the rest. See HRSupplyTarget.
	SupplyTarget float64 `json:"supply_target"`
	SupplyMin    float64 `json:"supply_min"`
	SupplyMax    float64 `json:"supply_max"`
}

// Efficiency of heat recovery, in percent.
type Efficiency struct {
	Supply  int `json:"supply"`
	Extract int `json:"extract"`
}

// Service describes the service reminder.
type Service struct {
	Enabled       bool `json:"enabled"`
	IntervalDays  int  `json:"interval_days"`
	DaysSince     int  `json:"days_since"`
	DaysRemaining int  `json:"days_remaining"`
}

// Machine identifies the unit itself.
type Machine struct {
	// Family is the model name, and FamilyCode the number it came from, kept
	// so an unrecognised unit can still say what it answered.
	Family     string `json:"family"`
	FamilyCode int    `json:"family_code"`
	// Serial is zero on a unit that never had one written, which is not rare.
	Serial int `json:"serial"`
}

// machineFamilies is the adapter's own list, in its own order.
//
// Taken from the original Freeway WEB interface, which maps HR597 to exactly
// these names.
var machineFamilies = []string{
	"Pingvin", "Pandion", "Pelican", "Pegasos", "Pegasos XL",
	"LTR-3", "LTR-6", "LTR-7", "LTR-7-XL",
}

func readMachine(h []uint16) Machine {
	code := int(h[HRMachineFamily])
	m := Machine{FamilyCode: code, Serial: int(h[HRSerialNumber])}
	if code >= 0 && code < len(machineFamilies) {
		m.Family = machineFamilies[code]
	}
	return m
}

// Output is what the unit is doing to the air, and how hard.
//
// Two registers, because the unit answers two questions. HR49 is the ladder:
// negative is cooling by that many per cent, 0 to 99 is heat recovery at that
// per cent, and 100 and above is recovery at full plus heating by the
// remainder. HR45 is the step the temperature control is on, which names
// states the ladder cannot express at all — starting up, stopped, defrosting,
// cleaning the exchanger.
//
// The ladder was measured, not assumed. In two consecutive readings HR49
// crossed from 101 to 102 and HR45 changed from 2 (recovery) to 4 (heating) at
// the same moment: the threshold is real and it is at 100.
type Output struct {
	// Recovery is how much of the heat in the extract air is being put back
	// into the supply air.
	Recovery int `json:"recovery"`
	// Heating is the additional heat being added once recovery is at full.
	Heating int `json:"heating"`
	// Cooling is how hard it is cooling, which never happens at the same time
	// as either of the others.
	Cooling int `json:"cooling"`
	// State names the step, for the ones that are not a proportion of
	// anything. Empty for a step the register list does not name.
	State string `json:"state"`
	// Step is the raw HR45 value, so an unnamed one is still reported.
	Step int `json:"step"`
}

// The steps HR45 names. Values 3 and above 10 the register list does not.
const (
	OutputNone      = "none"
	OutputCooling   = "cooling"
	OutputRecovery  = "recovery"
	OutputHeating   = "heating"
	OutputStepDelay = "step_delay"
	OutputSummerNC  = "summer_night_cooling"
	OutputStartup   = "startup"
	OutputStopped   = "stopped"
	OutputHRClean   = "hr_clean"
	OutputDefrost   = "defrost"
)

var outputSteps = map[int]string{
	0: OutputNone, 1: OutputCooling, 2: OutputRecovery, 4: OutputHeating,
	5: OutputStepDelay, 6: OutputSummerNC, 7: OutputStartup, 8: OutputStopped,
	9: OutputHRClean, 10: OutputDefrost,
}

// readOutput unpacks the ladder in HR49 and names the step in HR45.
func readOutput(ladder int16, step int) Output {
	o := Output{State: outputSteps[step], Step: step}
	switch {
	case ladder < 0:
		o.Cooling = clampPercent(int(-ladder))
	case ladder < 100:
		o.Recovery = int(ladder)
	default:
		o.Recovery = 100
		o.Heating = clampPercent(int(ladder) - 100)
	}
	return o
}

func clampPercent(v int) int {
	if v > 100 {
		return 100
	}
	if v < 0 {
		return 0
	}
	return v
}

// Link describes the state of the connection to the unit, which the interface
// shows as a coloured dot. The old adapter showed nothing at all: when it could
// not reach the unit, the page simply held the last values.
type Link struct {
	// Status is ok, degraded or down.
	Status string `json:"status"`
	Label  string `json:"label"`
	// Detail says why, when the status is not ok.
	Detail string `json:"detail,omitempty"`
	// DetailArg is the number a detail needs, where it needs one.
	DetailArg float64 `json:"-"`
	// ErrorRate is the share of recent attempts on the wire that failed.
	ErrorRate float64 `json:"error_rate"`
}

// Link statuses.
const (
	LinkOK       = "ok"
	LinkDegraded = "degraded"
	LinkDown     = "down"
)

// DegradedErrorRate is where a working line becomes a worrying one, and
// HealthyErrorRate where a worrying one is forgiven again.
//
// Measurement found a few per thousand with the adapter's latency timer at 1 ms,
// and three to five per hundred with it at the default 16, so two per cent is
// well clear of healthy and well below broken.
//
// Two thresholds rather than one, because the error rate is the share of the
// last 256 attempts — about seven minutes — and a line sitting near the mark
// crosses it repeatedly as failures age out of the window. One threshold made
// that a warning that came and went every few minutes, and a warning that
// blinks is one people learn to ignore. Going back to healthy now takes the
// line actually getting better rather than merely wobbling.
const (
	DegradedErrorRate = 0.02
	HealthyErrorRate  = 0.01

	// DegradedAfter is how long the rate has to stay past DegradedErrorRate
	// before the line is called unstable.
	//
	// What the warning is for is a connector working loose, which is a rate
	// that climbs and stays. What set it off in practice was a burst: six
	// failed requests in a few minutes around the unit changing what it was
	// doing, every one of them retried, the rate over the mark for less than a
	// minute — and "unstable connection" on a line that had been below one per
	// cent all night. A loose connector sits at three to five per cent for as
	// long as it is loose; five minutes tells the two apart.
	DegradedAfter = 5 * time.Minute
)

// State is everything the interface shows, decoded from one snapshot.
type State struct {
	// Name is what this unit is called. The unit cannot tell us: neither the
	// EDA register list nor the MD ones carry a model, name or serial number.
	// So it is whatever its owner decided to call it.
	Name string `json:"name"`
	Link Link   `json:"link"`

	// Taken is when the snapshot finished, and AgeSeconds how long ago that
	// was. Both are shown: a value with an honest age is more useful than one
	// that pretends to be live.
	Taken      time.Time `json:"taken"`
	AgeSeconds float64   `json:"age_seconds"`
	// Stale is true when the snapshot is older than it should be, which is how
	// the interface says the unit is not answering rather than showing numbers
	// as though nothing were wrong.
	Stale bool `json:"stale"`
	// Incomplete is true when some blocks of the last pass failed.
	Incomplete bool `json:"incomplete"`

	Temperatures Temperatures `json:"temperatures"`
	Humidity     int          `json:"humidity"`
	// ControlStep is holding register 45, how hard the unit is currently
	// working to reach the setpoint. Worth graphing: it explains the others.
	ControlStep int     `json:"control_step"`
	Setpoint    float64 `json:"setpoint"`
	// SetpointMin and SetpointMax are the unit's own limits, from holding
	// registers 140 and 141, in whole degrees. Narrower than the register
	// list's range: the unit measured allowed a span eight degrees wide where
	// the list says twenty.
	SetpointMin float64 `json:"setpoint_min"`
	SetpointMax float64 `json:"setpoint_max"`

	Mode  Mode   `json:"mode"`
	Flags []Flag `json:"flags"`

	FanSetpoint int  `json:"fan_setpoint"`
	FanActual   int  `json:"fan_actual"`
	ECFans      bool `json:"ec_fans"`

	Efficiency   Efficiency `json:"efficiency"`
	Output       Output     `json:"output"`
	Heating      bool       `json:"heating"`
	Cooling      bool       `json:"cooling"`
	HeatRecovery bool       `json:"heat_recovery"`
	Defrosting   bool       `json:"defrosting"`
	// Overpressure is the unit's own report that it is running it, taken from
	// the state word rather than from the coil beside Mode. The coil says what
	// was asked for; this says what is happening, which is the question worth
	// recording — overpressure also starts from a cooker hood switch and ends
	// by itself when its minutes run out, and neither of those is a press
	// anybody made.
	//
	// It lags the coil by the better part of ten seconds, which is why Mode is
	// read from the coils and this is not.
	Overpressure bool `json:"overpressure"`

	Alarm bool `json:"alarm"`
	// ActiveAlarms names what is wrong. The coil above only says that
	// something is, which is little help to somebody standing in a cold
	// house wondering why.
	ActiveAlarms []Alarm `json:"active_alarms"`
	// TimeProgramRunning is coil 43: a timer program is in effect right
	// now. It reports, it does not control — there is no switch for timer
	// programs, and a write to it is refused by the unit.
	TimeProgramRunning bool    `json:"time_program_running"`
	Service            Service `json:"service"`

	OverpressureMinutes int `json:"overpressure_minutes"`
	// BoostMinutes and BoostLevel are what boost does when it runs. The
	// unit cannot report how long is left, exactly as with overpressure.
	BoostMinutes int `json:"boost_minutes"`
	BoostLevel   int `json:"boost_level"`
	// OverpressureSupply and OverpressureExtract are the fan levels the unit
	// uses while overpressure runs. The gap between them is what makes the
	// pressure: for example 50 against 30.
	OverpressureSupply  int `json:"overpressure_supply"`
	OverpressureExtract int `json:"overpressure_extract"`
	// ModeRemaining is seconds left of whichever timed mode is running —
	// overpressure or boost — or null when one is in progress whose start we
	// did not see. The unit cannot tell us for either: register 56 is the
	// duration rather than the time left, and does not count down.
	ModeRemaining *float64 `json:"mode_remaining_seconds"`

	SoftwareVersion int `json:"software_version"`
	// Machine is what this unit is, which the panel does not show and the
	// register list does not name.
	Machine Machine `json:"machine"`
	// HumidityMean is the 48-hour mean of the extract humidity, and CO2 the
	// room sensor in ppm — zero on a unit that has none.
	HumidityMean int `json:"humidity_mean"`
	CO2          int `json:"co2"`
	// UnitClock is the unit's own clock, which drives its timer programs. It
	// is shown because it drifts, and a drift there means programs fire at the
	// wrong time.
	UnitClock      time.Time     `json:"unit_clock"`
	UnitClockDrift time.Duration `json:"-"`
	DriftSeconds   float64       `json:"unit_clock_drift_seconds"`
	// ClockDriftIsDST marks a drift of very nearly an hour, which on this
	// unit means the summer-time change rather than a clock wandering.
	// It has to be set on the panel twice a year, so recognising the
	// occasion lets the interface say what actually happened instead of
	// reporting a mysterious hour.
	ClockDriftIsDST bool `json:"clock_drift_is_dst"`
	// ClockSettable is nil until somebody has tried. This unit answers a
	// clock write without applying it, so the interface finds out by
	// attempting once rather than by assuming for every EDA board.
	ClockSettable *bool `json:"clock_settable"`

	// UnitWeekday is holding register 43, and UnitWeekdayExpected is what the
	// unit's own date says it should be. They disagreed by two days on the unit
	// measured.
	//
	// It is reported for diagnosis and used for nothing. A weekly programme set
	// for the weekday the date gave was measured firing, while the register
	// named a different day — so the unit schedules by its date and this
	// register is a broken mirror. Guarding on it would refuse edits that work
	// perfectly.
	UnitWeekday         int  `json:"unit_weekday"`
	UnitWeekdayExpected int  `json:"unit_weekday_expected"`
	WeekdayOK           bool `json:"weekday_ok"`
}

// Decode turns a snapshot into state. staleAfter is how old a snapshot may be
// before the interface should say so.
func Decode(s *bus.Snapshot, staleAfter time.Duration) (*State, error) {
	if s == nil {
		return nil, fmt.Errorf("eda: no snapshot yet")
	}
	if len(s.Holding) <= HRDaysSinceService || len(s.Coils) <= CoilHeatingAllowed {
		return nil, fmt.Errorf("eda: snapshot covers %d registers and %d coils, too few to decode",
			len(s.Holding), len(s.Coils))
	}

	age := time.Since(s.Finished)
	st := &State{
		Taken:      s.Finished,
		AgeSeconds: age.Seconds(),
		Stale:      age > staleAfter,
		Incomplete: !s.Complete(),

		Temperatures: Temperatures{
			FreshAir:            deci(s.Holding[HRFreshAir]),
			SupplyAfterRecovery: deci(s.Holding[HRSupplyAfter]),
			Supply:              deci(s.Holding[HRSupply]),
			Waste:               deci(s.Holding[HRWaste]),
			Extract:             deci(s.Holding[HRExtract]),
			Panel:               deci(s.Holding[HRPanelTemp]),
			RoomMean:            deci(s.Holding[HRRoomTempMean]),
			SupplyTarget:        deci(s.Holding[HRSupplyTarget]),
			SupplyMin:           deci(s.Holding[HRSupplyMin]),
			SupplyMax:           deci(s.Holding[HRSupplyMax]),
		},
		Machine:      readMachine(s.Holding),
		HumidityMean: int(s.Holding[HRHumidityMean]),
		CO2:          int(s.Holding[HRCO2]),
		Humidity:     int(s.Holding[HRHumidity]),
		ControlStep:  int(int16(s.Holding[HRControlStep])),
		Setpoint:     deci(s.Holding[HRSetpoint]),
		SetpointMin:  limit(s.Holding[HRSetpointMin], SetpointMin),
		SetpointMax:  limit(s.Holding[HRSetpointMax], SetpointMax),

		FanSetpoint: int(s.Holding[HRFanSetpoint]),
		FanActual:   int(s.Holding[HRFanActual]),
		ECFans:      s.Coils[CoilECFans],

		Output: readOutput(int16(s.Holding[HRCascadeI]), int(int16(s.Holding[HRControlStep]))),
		Efficiency: Efficiency{
			Supply:  int(s.Holding[HREfficiencySupply]),
			Extract: int(s.Holding[HREfficiencyExtract]),
		},
		Heating:      s.Coils[CoilHeating],
		Cooling:      s.Coils[CoilCooling],
		HeatRecovery: s.Coils[CoilHeatRecovery],

		Alarm:              s.Coils[CoilAlarmB],
		TimeProgramRunning: s.Coils[CoilTimeProgram],

		OverpressureMinutes: int(s.Holding[HROverpressureInUse]),
		OverpressureSupply:  int(s.Holding[HRSupplyOverpressure]),
		OverpressureExtract: int(s.Holding[HRExtractOverpressure]),
		BoostMinutes:        int(s.Holding[HRBoostMinutes]),
		BoostLevel:          int(s.Holding[HRBoostLevel]),
		SoftwareVersion:     int(s.Holding[HRSoftwareVersion]),
	}

	state := s.Holding[HRState]
	st.Defrosting = state&BitDefrosting != 0
	st.Overpressure = state&BitOverpressure != 0
	st.Flags = decodeFlags(state)
	st.Mode = decodeMode(s.Coils)

	interval := int(s.Holding[HRServiceInterval])
	since := int(s.Holding[HRDaysSinceService])
	st.Service = Service{
		Enabled:      s.Coils[CoilServiceReminder],
		IntervalDays: interval,
		DaysSince:    since,
		// Clamped at zero: past due is past due, and a negative number of days
		// reads as a bug rather than as a reminder.
		DaysRemaining: max(interval-since, 0),
	}

	if alarms, err := ReadAlarms(s.Holding); err == nil {
		st.ActiveAlarms = ActiveAlarms(alarms)
	}

	st.UnitClock = decodeClock(s.Holding)
	st.UnitWeekday = int(s.Holding[HRWeekday])
	st.UnitWeekdayExpected = -1
	if !st.UnitClock.IsZero() {
		// The unit counts Sunday as 0; Go counts it as 0 too.
		st.UnitWeekdayExpected = int(st.UnitClock.Weekday())
		st.WeekdayOK = st.UnitWeekday == st.UnitWeekdayExpected
	}
	if !st.UnitClock.IsZero() {
		// Against the moment the registers were read, not against now. A
		// snapshot is up to a poll interval old, and comparing its clock
		// reading to the present adds that whole interval to the drift — so a
		// unit synchronised to the second reported eight seconds out.
		// Against Started, not Finished. The clock registers are at the very
		// start of the map and are read in the first block of a pass that
		// takes a second and a half to finish, so measuring from the end adds
		// the whole pass to the drift — a unit two tenths of a second out read
		// as two seconds.
		drift := s.Started.Sub(st.UnitClock)
		if drift < 0 {
			drift = -drift
		}
		st.UnitClockDrift = drift
		st.DriftSeconds = drift.Seconds()
		// Within five minutes of an hour: a clock that merely drifts does not
		// land there, and a summer-time change lands nowhere else.
		if d := drift - time.Hour; d > -5*time.Minute && d < 5*time.Minute {
			st.ClockDriftIsDST = true
		}
	}
	return st, nil
}

// limit reads one of the unit's own setpoint bounds, falling back to the range
// the register list gives when the unit holds something implausible.
func limit(v uint16, fallback float64) float64 {
	d := float64(int16(v))
	if d < SetpointMin || d > SetpointMax {
		return fallback
	}
	return d
}

// deci converts a register holding a value scaled by ten. Temperatures below
// zero arrive as a two's complement value, so the register is read signed.
func deci(v uint16) float64 { return float64(int16(v)) / 10 }

func decodeFlags(state uint16) []Flag {
	var out []Flag
	for _, f := range flagNames {
		if state&f.bit != 0 {
			out = append(out, Flag{Bit: f.bit, Name: f.name, Label: f.label})
		}
	}
	return out
}

// decodeMode reads the mode from the coils rather than from the state bit
// field, because the bit field lags the coils by the better part of ten
// seconds and a mode that flickers back after a press looks broken.
func decodeMode(coils []bool) Mode {
	for _, m := range []Mode{ModeStop, ModeOverpressure, ModeBoost, ModeMaxHeating, ModeMaxCooling, ModeLongAway, ModeAway} {
		if coils[modeCoils[m]] {
			return m
		}
	}
	return ModeNormal
}

// decodeClock reads the unit's real-time clock. It returns the zero time if the
// registers do not describe a real date, which a unit that has lost power can
// produce.
// DecodeClockBlock reads the running clock out of the seven registers starting
// at HRSeconds, for a caller that has just read them straight off the bus
// rather than out of a snapshot.
func DecodeClockBlock(regs []uint16) time.Time {
	if len(regs) < 7 {
		return time.Time{}
	}
	h := make([]uint16, HRWeekday+1)
	copy(h[HRSeconds:], regs)
	return decodeClock(h)
}

func decodeClock(h []uint16) time.Time {
	sec, min, hour := int(h[HRSeconds]), int(h[HRMinutes]), int(h[HRHours])
	day, month, year := int(h[HRDay]), int(h[HRMonth]), 2000+int(h[HRYear])
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || min > 59 || sec > 59 {
		return time.Time{}
	}
	t := time.Date(year, time.Month(month), day, hour, min, sec, 0, time.Local)
	// time.Date normalises nonsense such as 31 February rather than refusing
	// it, so a round trip is what actually validates the date.
	if t.Day() != day || int(t.Month()) != month {
		return time.Time{}
	}
	return t
}
