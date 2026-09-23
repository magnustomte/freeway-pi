package eda

import (
	"fmt"

	"freewaypi/internal/i18n"
)

// Timer programs. The unit holds twenty weekly programs from holding register
// 210, six registers each, and five yearly ones from 330, eleven each. Coil 43
// turns the lot on and off.
//
// All the registers of one program are adjacent, so a program is written in a
// single operation and cannot be left half changed — a start time applied
// without its stop time would run until somebody noticed.
const (
	WeekProgramCount  = 20
	WeekProgramBase   = 210
	WeekProgramStride = 6
	YearProgramCount  = 5
	YearProgramBase   = 330
	YearProgramStride = 11
)

// Day bits in a weekly program's mask.
//
// The register list describes this contradictorily ("Sun is 1 = 1000000, Tue
// is 4 = 0010000"). Two programs on a live unit settled it: masks 62 and 65,
// with the same times, which can only be weekdays and weekend. So bit 0 is
// Sunday, matching holding register 43.
const (
	Sunday = 1 << iota
	Monday
	Tuesday
	Wednesday
	Thursday
	Friday
	Saturday
)

// AllDays is every day selected.
const AllDays = 127

// ProgramFunction is what a timer program does while it runs.
type ProgramFunction int

// The documented functions. Values 8 to 15 are AC fan speeds and are left out:
// the unit measured has EC fans, where the value is the percentage itself.
const (
	FunctionNone            ProgramFunction = 0
	FunctionAway            ProgramFunction = 1
	FunctionLongAway        ProgramFunction = 2
	FunctionHeatingBlocked  ProgramFunction = 3
	FunctionCoolingBlocked  ProgramFunction = 4
	FunctionTemperatureDrop ProgramFunction = 5
	FunctionMaxHeating      ProgramFunction = 6
	FunctionMaxCooling      ProgramFunction = 7
	FunctionRelay           ProgramFunction = 16
)

// FunctionChoice is one option the interface offers.
type FunctionChoice struct {
	Value ProgramFunction `json:"value"`
	Label string          `json:"label"`
	Help  string          `json:"help,omitempty"`
}

// FunctionChoices lists what a program can be set to, in the order to offer
// them. Fan speeds are a range rather than a list and are handled separately.
var FunctionChoices = []FunctionChoice{
	{FunctionNone, "program.function.none.label", "program.function.none.help"},
	{FunctionTemperatureDrop, "program.function.temperature_drop.label", "program.function.temperature_drop.help"},
	{FunctionHeatingBlocked, "program.function.heating_blocked.label", ""},
	{FunctionCoolingBlocked, "program.function.cooling_blocked.label", ""},
	{FunctionMaxHeating, "program.function.max_heating.label", ""},
	{FunctionMaxCooling, "program.function.max_cooling.label", ""},
	{FunctionAway, "program.function.away.label", "program.function.away.help"},
	{FunctionLongAway, "program.function.long_away.label", "program.function.long_away.help"},
	{FunctionRelay, "program.function.relay.label", ""},
}

// FanFunction turns an EC fan percentage into the value a program holds.
func FanFunction(percent int) (ProgramFunction, error) {
	if percent < FanMin || percent > FanMax {
		return 0, i18n.Errf("error.program.fan", FanMin, FanMax)
	}
	return ProgramFunction(percent), nil
}

// IsFanSpeed reports whether a function value is an EC fan percentage.
func IsFanSpeed(f ProgramFunction) bool { return f >= FanMin && f <= FanMax }

// WeekProgram is one weekly schedule entry.
type WeekProgram struct {
	Index int `json:"index"`
	// Days is a bit mask; see Sunday through Saturday.
	Days      int             `json:"days"`
	StartHour int             `json:"start_hour"`
	StartMin  int             `json:"start_minute"`
	StopHour  int             `json:"stop_hour"`
	StopMin   int             `json:"stop_minute"`
	Function  ProgramFunction `json:"function"`
}

// Active reports whether the program does anything at all.
func (p WeekProgram) Active() bool { return p.Days != 0 && p.Function != FunctionNone }

// YearProgram is one dated schedule entry.
type YearProgram struct {
	Index      int             `json:"index"`
	StartDay   int             `json:"start_day"`
	StartMonth int             `json:"start_month"`
	StartYear  int             `json:"start_year"`
	StartHour  int             `json:"start_hour"`
	StartMin   int             `json:"start_minute"`
	StopDay    int             `json:"stop_day"`
	StopMonth  int             `json:"stop_month"`
	StopYear   int             `json:"stop_year"`
	StopHour   int             `json:"stop_hour"`
	StopMin    int             `json:"stop_minute"`
	Function   ProgramFunction `json:"function"`
}

// Active reports whether the program does anything at all.
func (p YearProgram) Active() bool {
	return p.Function != FunctionNone && p.StartMonth != 0 && p.StopMonth != 0
}

// ReadWeekPrograms pulls all twenty out of a snapshot.
func ReadWeekPrograms(holding []uint16) ([]WeekProgram, error) {
	last := WeekProgramBase + WeekProgramCount*WeekProgramStride
	if len(holding) < last {
		return nil, fmt.Errorf("eda: snapshot reaches %d, the weekly programs end at %d", len(holding), last)
	}
	out := make([]WeekProgram, WeekProgramCount)
	for i := range out {
		b := WeekProgramBase + i*WeekProgramStride
		out[i] = WeekProgram{
			Index:     i,
			Days:      int(holding[b]),
			StartHour: int(holding[b+1]), StartMin: int(holding[b+2]),
			StopHour: int(holding[b+3]), StopMin: int(holding[b+4]),
			Function: ProgramFunction(holding[b+5]),
		}
	}
	return out, nil
}

// ReadYearPrograms pulls all five out of a snapshot.
func ReadYearPrograms(holding []uint16) ([]YearProgram, error) {
	last := YearProgramBase + YearProgramCount*YearProgramStride
	if len(holding) < last {
		return nil, fmt.Errorf("eda: snapshot reaches %d, the yearly programs end at %d", len(holding), last)
	}
	out := make([]YearProgram, YearProgramCount)
	for i := range out {
		b := YearProgramBase + i*YearProgramStride
		out[i] = YearProgram{
			Index:    i,
			StartDay: int(holding[b]), StartMonth: int(holding[b+1]), StartYear: 2000 + int(holding[b+2]),
			StartHour: int(holding[b+3]), StartMin: int(holding[b+4]),
			StopDay: int(holding[b+5]), StopMonth: int(holding[b+6]), StopYear: 2000 + int(holding[b+7]),
			StopHour: int(holding[b+8]), StopMin: int(holding[b+9]),
			Function: ProgramFunction(holding[b+10]),
		}
	}
	return out, nil
}

// WriteWeekProgram returns the single write that stores one weekly program.
func WriteWeekProgram(p WeekProgram) ([]Write, error) {
	if p.Index < 0 || p.Index >= WeekProgramCount {
		return nil, i18n.Errf("error.program.week.missing", p.Index+1)
	}
	if p.Days < 0 || p.Days > AllDays {
		return nil, fmt.Errorf("eda: ugyldig dagvalg")
	}
	if err := checkTime(p.StartHour, p.StartMin); err != nil {
		return nil, fmt.Errorf("starttid: %w", err)
	}
	if err := checkTime(p.StopHour, p.StopMin); err != nil {
		return nil, fmt.Errorf("stopptid: %w", err)
	}
	if err := checkFunction(p.Function); err != nil {
		return nil, err
	}
	return []Write{{
		Addr: uint16(WeekProgramBase + p.Index*WeekProgramStride),
		Regs: []uint16{
			uint16(p.Days), uint16(p.StartHour), uint16(p.StartMin),
			uint16(p.StopHour), uint16(p.StopMin), uint16(p.Function),
		},
		Describe: fmt.Sprintf("week program %d: %02d:%02d–%02d:%02d, %s",
			p.Index+1, p.StartHour, p.StartMin, p.StopHour, p.StopMin, FunctionName(p.Function)),
	}}, nil
}

// WriteYearProgram returns the single write that stores one yearly program.
func WriteYearProgram(p YearProgram) ([]Write, error) {
	if p.Index < 0 || p.Index >= YearProgramCount {
		return nil, i18n.Errf("error.program.year.missing", p.Index+1)
	}
	for _, d := range []struct {
		day, month, year, hour, min int
		what                        string
	}{
		{p.StartDay, p.StartMonth, p.StartYear, p.StartHour, p.StartMin, "startdato"},
		{p.StopDay, p.StopMonth, p.StopYear, p.StopHour, p.StopMin, "stoppdato"},
	} {
		if err := checkDate(d.day, d.month, d.year); err != nil {
			return nil, fmt.Errorf("%s: %w", d.what, err)
		}
		if err := checkTime(d.hour, d.min); err != nil {
			return nil, fmt.Errorf("%s: %w", d.what, err)
		}
	}
	if err := checkFunction(p.Function); err != nil {
		return nil, err
	}
	return []Write{{
		Addr: uint16(YearProgramBase + p.Index*YearProgramStride),
		Regs: []uint16{
			uint16(p.StartDay), uint16(p.StartMonth), uint16(p.StartYear - 2000),
			uint16(p.StartHour), uint16(p.StartMin),
			uint16(p.StopDay), uint16(p.StopMonth), uint16(p.StopYear - 2000),
			uint16(p.StopHour), uint16(p.StopMin),
			uint16(p.Function),
		},
		Describe: fmt.Sprintf("year program %d: %d.%d.%d–%d.%d.%d, %s",
			p.Index+1, p.StartDay, p.StartMonth, p.StartYear,
			p.StopDay, p.StopMonth, p.StopYear, FunctionName(p.Function)),
	}}, nil
}

// FunctionName renders a program function for people.
func FunctionName(f ProgramFunction) string {
	if IsFanSpeed(f) {
		return fmt.Sprintf("fan level %d %%", int(f))
	}
	for _, c := range FunctionChoices {
		if c.Value == f {
			return c.Label
		}
	}
	return fmt.Sprintf("funksjon %d", int(f))
}

func checkTime(hour, min int) error {
	if hour < 0 || hour > 23 {
		return i18n.Errf("error.time.hour")
	}
	if min < 0 || min > 59 {
		return i18n.Errf("error.time.minute")
	}
	return nil
}

func checkDate(day, month, year int) error {
	if month < 1 || month > 12 {
		return i18n.Errf("error.time.month")
	}
	if day < 1 || day > 31 {
		return i18n.Errf("error.time.day")
	}
	// The register holds the year less 2000 in a single byte's worth of range.
	if year < 2006 || year > 2100 {
		return i18n.Errf("error.time.year")
	}
	return nil
}

func checkFunction(f ProgramFunction) error {
	if IsFanSpeed(f) {
		return nil
	}
	for _, c := range FunctionChoices {
		if c.Value == f {
			return nil
		}
	}
	return i18n.Errf("error.program.function", int(f))
}

// ClearWeekProgram blanks a weekly program.
//
// The unit always has twenty; there is no removing one, only emptying it. All
// six registers go to zero in a single write, which leaves a slot the interface
// treats as free and the unit treats as doing nothing.
func ClearWeekProgram(index int) ([]Write, error) {
	if index < 0 || index >= WeekProgramCount {
		return nil, i18n.Errf("error.program.week.missing", index+1)
	}
	return []Write{{
		Addr:     uint16(WeekProgramBase + index*WeekProgramStride),
		Regs:     make([]uint16, WeekProgramStride),
		Describe: fmt.Sprintf("remove week program %d", index+1),
	}}, nil
}

// ClearYearProgram blanks a yearly program.
func ClearYearProgram(index int) ([]Write, error) {
	if index < 0 || index >= YearProgramCount {
		return nil, i18n.Errf("error.program.year.missing", index+1)
	}
	return []Write{{
		Addr:     uint16(YearProgramBase + index*YearProgramStride),
		Regs:     make([]uint16, YearProgramStride),
		Describe: fmt.Sprintf("remove year program %d", index+1),
	}}, nil
}
