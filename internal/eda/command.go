package eda

import (
	"fmt"

	"freewaypi/internal/i18n"
)

// MaxTimedMinutes bounds how long overpressure and boost may run.
//
// The register list gives 0 to 60 for both, and the unit enforces neither:
// measured, it accepted 90 in each and held it. So this is the only limit there
// is, which is a reason to have one rather than a reason to shrug. Overpressure
// in particular pushes air into the structure, and hours of it is not what the
// sixty minutes was written for.
const MaxTimedMinutes = 60

// Write is one write to send to the unit.
//
// Coil writes are always single. The mode coils are 0, 1, 2, 3, 6, 7 and 10,
// and the gaps between them are not spare: 4 and 5 are the cooker hood and the
// central vacuum, and 8, 9 and 11 are the bits that permit CO2, humidity and
// temperature boost. Writing the range as one block to save a few hundred
// milliseconds would silently turn those off.
type Write struct {
	// Coil selects between a coil write and a holding register write.
	Coil bool
	Addr uint16
	Bit  bool
	Regs []uint16
	// Describe is what the audit log records, in the operator's terms rather
	// than as an address.
	//
	// Not translated, and deliberately: a journal is read by whoever is
	// debugging the box, not by whoever pressed the button, and a log whose
	// language depends on which browser made the request is a log nobody can
	// grep. It never reaches a screen — the error a failed write produces says
	// what went wrong without repeating what was attempted.
	Describe string
}

// SetMode returns the writes that change the operating mode, given the mode the
// unit is in now.
//
// Only what has to change is written. The unit takes a noticeable moment per
// write, so clearing all six unrelated coils to select one would make a mode
// change visibly slow for no gain.
func SetMode(current, want Mode) ([]Write, error) {
	if _, ok := modeCoils[want]; !ok && want != ModeNormal {
		return nil, fmt.Errorf("eda: %q is not a mode", want)
	}
	if current == want {
		return nil, nil
	}

	var writes []Write
	if coil, ok := modeCoils[current]; ok {
		writes = append(writes, Write{
			Coil: true, Addr: coil, Bit: false,
			Describe: fmt.Sprintf("turn off %s", current),
		})
	}
	if coil, ok := modeCoils[want]; ok {
		writes = append(writes, Write{
			Coil: true, Addr: coil, Bit: true,
			Describe: fmt.Sprintf("turn on %s", want),
		})
	}
	return writes, nil
}

// SetSetpoint returns the write that changes the target temperature.
//
// The bounds come from the unit rather than from the register list, because the
// unit's own are narrower: this one allows 18 to 26 where the list says 10 to
// 30. Refusing what the panel would refuse beats writing a value the unit
// quietly ignores. Unusable bounds fall back to the documented range rather
// than locking the interface out of every value.
//
// lo and hi rather than min and max: those are builtins now, and shadowing
// them inside a function that does arithmetic is a trap for whoever edits it
// next.
func SetSetpoint(celsius, lo, hi float64) ([]Write, error) {
	if lo <= 0 || hi <= 0 || lo >= hi {
		lo, hi = SetpointMin, SetpointMax
	}
	if celsius < lo || celsius > hi {
		return nil, i18n.Errf("error.setpoint.range", celsius, lo, hi)
	}
	// Rounded to the tenth the register holds, so that 21.25 becomes 21.3
	// rather than being truncated to 21.2 by the conversion.
	v := uint16(int16(celsius*10 + 0.5))
	return []Write{{
		Addr: HRSetpoint, Regs: []uint16{v},
		Describe: fmt.Sprintf("set temperature to %.1f °C", float64(v)/10),
	}}, nil
}

// SetFanLevel returns the write that changes the fan level.
//
// Only EC fans are supported. AC fans use steps 1
// to 8 and a different meaning for the register, so guessing would be worse
// than refusing.
func SetFanLevel(ecFans bool, percent int) ([]Write, error) {
	if !ecFans {
		return nil, fmt.Errorf("eda: this unit has AC fans, which use steps rather than a percentage")
	}
	if percent < FanMin || percent > FanMax {
		return nil, fmt.Errorf("eda: %d%% is outside the range EC fans accept, %d to %d",
			percent, FanMin, FanMax)
	}
	return []Write{{
		Addr: HRFanSetpoint, Regs: []uint16{uint16(percent)},
		Describe: fmt.Sprintf("set fan level to %d%%", percent),
	}}, nil
}

// SetOverpressureDuration returns the write that changes how long overpressure
// runs.
//
// Both copies are written. The register list calls 56 the time remaining and
// read only, and 57 the setting; on the unit measured it is the other way
// round, and 56
// is what the unit acts on. They are adjacent, so both go in one write.
func SetOverpressureDuration(minutes int) ([]Write, error) {
	if minutes < 1 || minutes > MaxTimedMinutes {
		return nil, i18n.Errf("error.overpressure.minutes", minutes, MaxTimedMinutes)
	}
	v := uint16(minutes)
	return []Write{{
		Addr: HROverpressureInUse, Regs: []uint16{v, v},
		Describe: fmt.Sprintf("set overpressure duration to %d min", minutes),
	}}, nil
}

// StartOverpressure sets the duration and then turns overpressure on, in that
// order, so the unit cannot start a run on the previous duration.
func StartOverpressure(current Mode, minutes int) ([]Write, error) {
	duration, err := SetOverpressureDuration(minutes)
	if err != nil {
		return nil, err
	}
	mode, err := SetMode(current, ModeOverpressure)
	if err != nil {
		return nil, err
	}
	return append(duration, mode...), nil
}

// SetBoostDuration returns the write that changes how long boost runs.
//
// One register, unlike overpressure. HR56 and HR57 mirror each other and both
// have to be written; HR66 stands alone, with the level in 67 beside it.
// Measured: HR66 read 30, matching the panel, and a write was acknowledged and
// applied.
func SetBoostDuration(minutes int) ([]Write, error) {
	if minutes < 1 || minutes > MaxTimedMinutes {
		return nil, i18n.Errf("error.boost.minutes", minutes, MaxTimedMinutes)
	}
	return []Write{{
		Addr: HRBoostMinutes, Regs: []uint16{uint16(minutes)},
		Describe: fmt.Sprintf("set boost duration to %d min", minutes),
	}}, nil
}

// StartBoost sets the duration and then turns boost on, in that order, so the
// unit cannot start a run on the previous duration.
func StartBoost(current Mode, minutes int) ([]Write, error) {
	duration, err := SetBoostDuration(minutes)
	if err != nil {
		return nil, err
	}
	mode, err := SetMode(current, ModeBoost)
	if err != nil {
		return nil, err
	}
	return append(duration, mode...), nil
}

// SetHeatingAllowed and SetCoolingAllowed change configuration bits that
// persist across power cycles. They are what keeps a heat pump from heating in
// summer, and are not momentary commands.
func SetHeatingAllowed(allowed bool) []Write {
	return []Write{{Coil: true, Addr: CoilHeatingAllowed, Bit: allowed,
		Describe: fmt.Sprintf("varme tillatt: %v", allowed)}}
}

// SetCoolingAllowed is the counterpart to SetHeatingAllowed.
func SetCoolingAllowed(allowed bool) []Write {
	return []Write{{Coil: true, Addr: CoilCoolingAllowed, Bit: allowed,
		Describe: fmt.Sprintf("cooling allowed: %v", allowed)}}
}

// SetClock returns the write that sets the unit's real-time clock.
//
// Five adjacent registers in one write, so the clock cannot be left half set
// with the date changed and the time not.
//
// Not the running clock at HR37–43: that one accepts a write, reads it back,
// and is overwritten by the unit's own clock task a second later. This is the
// setting block, and writing it is what actually moves the clock.
//
// Seconds and weekday are absent because the block has no register for either.
// The unit zeroes the seconds when it applies the write and derives the weekday
// from the date, so t.Second and t.Weekday are ignored here — see SyncClock for
// what the missing seconds mean for when this may be sent.
func SetClock(t Time) []Write {
	return []Write{{
		Addr: HRSetMinute,
		Regs: []uint16{
			uint16(t.Minute), uint16(t.Hour),
			uint16(t.Day), uint16(t.Month), uint16(t.Year - 2000),
		},
		Describe: fmt.Sprintf("set the unit's clock to %02d:%02d %02d.%02d.%d",
			t.Hour, t.Minute, t.Day, t.Month, t.Year),
	}}
}

// Time is a clock reading in the unit's own terms.
//
// Second and Weekday are read from the running clock and are not settable: the
// setting block has no register for either.
type Time struct {
	Second, Minute, Hour int
	Day, Month, Year     int
	Weekday              int // Sunday 0 .. Saturday 6
}

// ResetServiceCounter sets the days-since-service counter back to zero, which
// is what somebody does when they have actually changed the filter.
//
// Separate from the interval, because they are separate registers and changing
// one does nothing to the other: setting a new interval only recomputes how far
// off the next reminder is from the same "days since". Measured: a write to
// HR710 was acknowledged and applied, and changing the interval left HR710
// untouched — the register list calls it R/W and here it is telling the truth.
func ResetServiceCounter() []Write {
	return []Write{{
		Addr: HRDaysSinceService, Regs: []uint16{0},
		Describe: "nullstill servicetelleren",
	}}
}
