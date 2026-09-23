package eda

import (
	"fmt"

	"freewaypi/internal/i18n"
	"math"
)

// Setting is one thing about the unit that a person may change and that stays
// changed. Everything here persists in the unit across a power cycle, which is
// what separates it from the daily controls.
type Setting struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Help  string `json:"help,omitempty"`
	Group string `json:"group"`
	// Type is "number" or "bool".
	Type string `json:"type"`
	Unit string `json:"unit,omitempty"`
	// Scale is what the register is multiplied by. Temperatures are usually
	// held at ten times their value; the setpoint limits, confusingly, are not.
	Scale float64 `json:"scale"`
	Min   float64 `json:"min,omitempty"`
	Max   float64 `json:"max,omitempty"`
	Step  float64 `json:"step,omitempty"`

	coil bool
	addr uint16
	// companion is a second register written with the same value. Overpressure
	// keeps its duration in two places and they must not disagree.
	companion uint16
	hasTwin   bool
}

// Settings is everything the advanced section offers, in the order it shows
// them. Anything not here is still reachable through the register explorer;
// this is the set worth naming and explaining.
//
// Every entry here is writable according to the register list, which is a
// thing to check rather than infer from a name. Coil 43 was offered as "timer
// programs in use" until the unit answered a write to it with server device
// failure: both register lists call it a status bit that reports whether a
// program is running at this moment, and it sits between the two alarm bits.
// There is no global switch for timer programs; a program is off when its
// function is set to none.
//
// Holding register 56 is the exception in the other direction. The list marks
// it read only, and measurement on a live unit showed that it is not.
var Settings = []Setting{
	{
		Key: "setpoint_min", Label: "setting.setpoint_min.label", Group: "setting.group.temperature",
		Help: "setting.setpoint_min.help",
		Type: "number", Unit: "°C", Scale: 1, Min: 10, Max: 30, Step: 1,
		addr: HRSetpointMin,
	},
	{
		Key: "setpoint_max", Label: "setting.setpoint_max.label", Group: "setting.group.temperature",
		Type: "number", Unit: "°C", Scale: 1, Min: 10, Max: 30, Step: 1,
		addr: HRSetpointMax,
	},
	{
		Key: "cooling_block_below", Label: "setting.cooling_block_below.label", Group: "setting.group.temperature",
		Help: "setting.cooling_block_below.help",
		Type: "number", Unit: "°C", Scale: 10, Min: -30, Max: 40, Step: 0.5,
		addr: HRCoolingBlockBelow,
	},
	{
		Key: "heating_block_above", Label: "setting.heating_block_above.label", Group: "setting.group.temperature",
		Help: "setting.heating_block_above.help",
		Type: "number", Unit: "°C", Scale: 10, Min: -30, Max: 40, Step: 0.5,
		addr: HRHeatingBlockAbove,
	},
	{
		Key: "temperature_drop", Label: "setting.temperature_drop.label", Group: "setting.group.temperature",
		Help: "setting.temperature_drop.help",
		Type: "number", Unit: "°C", Scale: 10, Min: 0, Max: 10, Step: 0.5,
		addr: HRTemperatureDrop,
	},

	{
		Key: "heating_allowed", Label: "setting.heating_allowed.label", Group: "setting.group.permissions",
		Help: "setting.heating_allowed.help",
		Type: "bool", coil: true, addr: CoilHeatingAllowed,
	},
	{
		Key: "cooling_allowed", Label: "setting.cooling_allowed.label", Group: "setting.group.permissions",
		Type: "bool", coil: true, addr: CoilCoolingAllowed,
	},

	{
		Key: "overpressure_minutes", Label: "setting.overpressure_minutes.label", Group: "setting.group.overpressure",
		Help: "setting.overpressure_minutes.help",
		Type: "number", Unit: "min", Scale: 1, Min: 1, Max: 60, Step: 1,
		addr: HROverpressureInUse, companion: HROverpressureAlt, hasTwin: true,
	},
	{
		Key: "overpressure_supply", Label: "setting.overpressure_supply.label", Group: "setting.group.overpressure",
		Help: "setting.overpressure_supply.help",
		Type: "number", Unit: "%", Scale: 1, Min: 20, Max: 100, Step: 5,
		addr: HRSupplyOverpressure,
	},
	{
		Key: "overpressure_extract", Label: "setting.overpressure_extract.label", Group: "setting.group.overpressure",
		Type: "number", Unit: "%", Scale: 1, Min: 20, Max: 100, Step: 5,
		addr: HRExtractOverpressure,
	},

	{
		Key: "boost_minutes", Label: "setting.boost_minutes.label", Group: "setting.group.boost",
		Help: "setting.boost_minutes.help",
		Type: "number", Unit: "min", Scale: 1, Min: 1, Max: 60, Step: 5,
		addr: HRBoostMinutes,
	},
	{
		Key: "boost_level", Label: "setting.boost_level.label", Group: "setting.group.boost",
		Help: "setting.boost_level.help",
		Type: "number", Unit: "%", Scale: 1, Min: 20, Max: 100, Step: 5,
		addr: HRBoostLevel,
	},

	{
		Key: "service_reminder", Label: "setting.service_reminder.label", Group: "setting.group.service",
		Type: "bool", coil: true, addr: CoilServiceReminder,
	},
	{
		Key: "service_interval_days", Label: "setting.service_interval_days.label", Group: "setting.group.service",
		Type: "number", Unit: "ui.unit.days", Scale: 1, Min: 1, Max: 999, Step: 1,
		addr: HRServiceInterval,
	},

	{
		Key: "measurement_refresh", Label: "setting.measurement_refresh.label", Group: "setting.group.advanced",
		Help: "setting.measurement_refresh.help",
		Type: "number", Unit: "s", Scale: 1, Min: 1, Max: 60, Step: 1,
		addr: HRMeasurementRefresh,
	},
}

// SettingByKey looks one up.
func SettingByKey(key string) (Setting, bool) {
	for _, s := range Settings {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}

// SettingValue is a setting together with what the unit currently holds.
type SettingValue struct {
	Setting
	// GroupKey is the group's catalogue id, kept beside the translated name.
	// The interface groups by it: matching on the words would break the moment
	// somebody read the page in the other language.
	GroupKey string  `json:"group_key"`
	Number   float64 `json:"number,omitempty"`
	Bool     bool    `json:"bool,omitempty"`
}

// ReadSettings pulls the current values out of a snapshot. No bus traffic: the
// poller has already read every register these live in.
func ReadSettings(holding []uint16, coils []bool) ([]SettingValue, error) {
	out := make([]SettingValue, 0, len(Settings))
	for _, s := range Settings {
		v := SettingValue{Setting: s, GroupKey: s.Group}
		if s.coil {
			if int(s.addr) >= len(coils) {
				return nil, fmt.Errorf("eda: coil %d is outside the snapshot", s.addr)
			}
			v.Bool = coils[s.addr]
		} else {
			if int(s.addr) >= len(holding) {
				return nil, fmt.Errorf("eda: holding register %d is outside the snapshot", s.addr)
			}
			v.Number = float64(int16(holding[s.addr])) / s.Scale
		}
		out = append(out, v)
	}
	return out, nil
}

// WriteSetting turns a new value into the writes that apply it.
func WriteSetting(key string, number float64, boolean bool) ([]Write, error) {
	s, ok := SettingByKey(key)
	if !ok {
		return nil, fmt.Errorf("eda: %q is not a setting", key)
	}
	if s.coil {
		return []Write{{
			Coil: true, Addr: s.addr, Bit: boolean,
			Describe: fmt.Sprintf("%s: %v", s.Label, boolean),
		}}, nil
	}

	if number < s.Min || number > s.Max {
		return nil, i18n.Errf("error.setting.range", i18n.Msg(s.Label), s.Min, s.Max, i18n.Msg(s.Unit))
	}
	raw := uint16(int16(math.Round(number * s.Scale)))
	regs := []uint16{raw}
	addr := s.addr
	// The twin is adjacent, so both copies go in one write and cannot end up
	// disagreeing.
	if s.hasTwin {
		if s.companion == s.addr+1 {
			regs = append(regs, raw)
		} else {
			return nil, i18n.Errf("error.setting.twin", i18n.Msg(s.Label))
		}
	}
	return []Write{{
		Addr: addr, Regs: regs,
		Describe: fmt.Sprintf("%s: %g %s", s.Label, number, s.Unit),
	}}, nil
}
