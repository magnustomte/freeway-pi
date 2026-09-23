// Package eda turns the unit's register map into something an interface can
// show, and turns a person's intent back into writes.
//
// The authoritative register list is Enervent's own document, kept with the
// Homey app rather than duplicated here. Only the addresses this code actually
// uses appear below, because code cannot be written without them; anything that
// needs explaining is explained there, not here.
//
// Two addresses come from the MD register lists rather than the EDA one, which
// stops at 651: holding register 710 is days since the service reminder was
// acknowledged, and 726 is the interval at which the unit refreshes its own
// measurement registers. Both were confirmed on a live unit, along with
// 733 and 734 reading exactly the 19200 bps and no parity the link runs at,
// which is what makes the MD lists trustworthy for that range.
package eda

// Holding registers.
// The clock is two blocks, and only one of them sets it.
//
// HR37–43 is the running clock. It can be written — the unit acknowledges it
// and the value reads back — but the unit's own clock task overwrites it within
// a second or two, so nothing comes of it. That is why setting the clock
// appeared not to work at all, and why an immediate read-back said it had.
//
// HR582–586 is the setting block: minute, hour, day, month, year. Writing it
// sets the running clock, and the block keeps what was written. It is the same
// five fields as HR38–42 offset by 544, and it is what the original Freeway WEB
// adapter writes — found by reading its interface, then confirmed on a live
// unit.
//
// It has no seconds register, and the unit zeroes the seconds when it applies
// the write. A clock set at an arbitrary moment therefore lands up to a minute
// behind; the write has to be timed to a minute boundary, which is what
// control.SyncClock does.
const (
	HRSeconds = 37
	HRMinutes = 38
	HRHours   = 39
	HRDay     = 40
	HRMonth   = 41
	HRYear    = 42
	HRWeekday = 43

	// The setting block. No seconds and no weekday: the unit derives both.
	HRSetMinute = 582
	HRSetHour   = 583
	HRSetDay    = 584
	HRSetMonth  = 585
	HRSetYear   = 586

	// HRPanelTemp is the sensor in the control panel, and HRRoomTempMean the
	// mean room temperature the unit works from — on a unit whose only room
	// sensor is the panel, the two read the same.
	//
	// Neither is in the register list under a name that gives them away. The
	// original adapter's measurements page reads a block of sixteen registers
	// and labels them; eight of those are ones we already knew, which is what
	// makes the rest trustworthy. Confirmed: the two read identically, and both
	// differed from the extract air.
	HRPanelTemp    = 1  // T_OP1
	HRRoomTempMean = 46 // MEAN_TEMP
	HRHumidityMean = 35 // RH_MEAN, 48-hour mean of the extract humidity
	HRCO2          = 27 // AI5_RES, ppm; zero on a unit with no CO2 sensor

	HRFreshAir    = 6  // X1, outdoor
	HRSupplyAfter = 7  // X2, supply after heat recovery
	HRSupply      = 8  // X3, supply to the rooms
	HRWaste       = 9  // X4, thrown out
	HRExtract     = 10 // X5, taken from the rooms
	HRHumidity    = 13 // X5 extract air humidity, %

	// HRCascadeI is the integral term of the cascade temperature controller.
	//
	// It is NOT what the unit is doing to the air, although this file said so
	// for a while and the interface drew three symbols from it. The register
	// list is unambiguous — 3x0047 Cascade SP, 3x0048 Cascade P, 3x0049
	// Cascade I, described in the list as the integral term of the cascade
	// control — and the data agrees: over two days of recorded history it
	// stayed in single digits while the heating coil ran repeatedly and
	// recovery efficiency was high.
	//
	// The mistake is worth keeping written down. It was inferred from the
	// original adapter drawing three symbols, and "confirmed" by one live
	// negative reading while the unit was cooling. A negative integral term is
	// exactly what a controller pushing the temperature down looks like, so
	// the one observation fitted both readings and settled nothing. What the
	// unit is actually doing is HRControlStep below.
	HRCascadeI = 49

	// HRSupplyTarget is the supply air temperature the cascade controller has
	// decided on, and HRSupplyMin/Max the limits it may choose between.
	//
	// The unit does not heat the room directly: an outer loop compares the room
	// with the setpoint and computes a target for the supply air, and an inner
	// loop hits that target with the exchanger, the heater or the cooler. This
	// is that target, and it is the answer to "why is the supply so cold when I
	// asked for 22" — measured with the room above the setpoint, the target sat
	// at exactly HRSupplyMin. It had bottomed out, deliberately.
	HRSupplyTarget = 47  // Cascade SP
	HRSupplyMin    = 138 // SPLY T MIN
	HRSupplyMax    = 139 // SPLY T MAX

	HREfficiencySupply  = 29
	HREfficiencyExtract = 30

	HRState = 44 // bit field
	// HRControlStep is which step the temperature control is on, and so what
	// the unit is doing to the air: 0 nothing, 1 cooling, 2 heat recovery,
	// 4 heating, 5 waiting to change step, 6 summer night cooling, 7 starting
	// up, 8 stopped, 9 heat recovery cleaning, 10 external unit defrost. The
	// register list calls it the control steps of temperature; 3 and anything
	// above 10 it does not name.
	//
	// A state, never a proportion. The unit says which of these it is doing
	// and never how hard, so an interface that shows a percentage here is
	// showing a number nobody measured.
	HRControlStep = 45
	HRFanActual   = 50 // in effect, read only
	HRFanSetpoint = 53 // set on the panel, writable
	// Boost, which the manual calls forcering: it lifts both fans for a
	// period. Factory settings are 90 % for 30 minutes.
	HRBoostMinutes = 66
	HRBoostLevel   = 67
	// HRBoostChangeable permits the level to be altered from the panel
	// while a boost runs.
	HRBoostChangeable = 68

	HRSupplyOverpressure  = 54 // supply fan level while overpressure runs
	HRExtractOverpressure = 55 // extract fan level while overpressure runs
	HROverpressureInUse   = 56 // the duration the unit acts on
	HROverpressureAlt     = 57 // the second copy; written together with 56
	HRSetpoint            = 135
	HRSetpointMin         = 140
	HRSetpointMax         = 141
	HRCoolingBlockBelow   = 164
	HRHeatingBlockAbove   = 196

	HRTemperatureDrop = 172 // how far a timer program lowers the setpoint

	HRAlarmType  = 385
	HRAlarmState = 386

	HRServiceInterval = 538
	HRSoftwareVersion = 599
	// HRMachineFamily is which Enervent machine this is, as a code, and
	// HRSerialNumber its serial — zero on a unit that never had one written.
	HRMachineFamily      = 597
	HRSerialNumber       = 598
	HRDaysSinceService   = 710
	HRMeasurementRefresh = 726
)

// Coils.
const (
	CoilStop            = 0
	CoilAway            = 1
	CoilLongAway        = 2
	CoilOverpressure    = 3
	CoilMaxHeating      = 6
	CoilMaxCooling      = 7
	CoilBoost           = 10
	CoilECFans          = 16 // 1 on EC fans, 0 on AC
	CoilCooling         = 28 // cooling in operation
	CoilHeatRecovery    = 30 // heat recovery running
	CoilHeating         = 32 // heating in operation
	CoilAlarmB          = 42 // any B alarm active
	CoilTimeProgram     = 43
	CoilServiceReminder = 49
	CoilCoolingAllowed  = 52
	CoilHeatingAllowed  = 54
)

// State bits in holding register 44. Several can be set at once; the register
// holds their sum. Read unsigned: the defrost bit makes a signed read negative.
const (
	BitMaxCooling       = 1 << 0
	BitMaxHeating       = 1 << 1
	BitEmergencyStop    = 1 << 2
	BitStop             = 1 << 3
	BitAway             = 1 << 4
	BitLongAway         = 1 << 5
	BitTemperatureBoost = 1 << 6
	BitCO2Boost         = 1 << 7
	BitHumidityBoost    = 1 << 8
	BitBoost            = 1 << 9
	BitOverpressure     = 1 << 10
	BitCookerHood       = 1 << 11
	BitCentralVacuum    = 1 << 12
	BitELHCooling       = 1 << 13
	BitSummerNightCool  = 1 << 14
	// BitDefrosting is named after the EDX product line but is also how a unit
	// with an integrated heat pump reports that it is defrosting.
	BitDefrosting = 1 << 15
)

// FanMin is the lowest level EC fans accept. A heat pump lifts the fans to 70%
// while it runs whatever the panel says, so the level in effect often differs
// from the one set, and that is the unit working rather than a fault.
const (
	FanMin = 20
	FanMax = 100
)

// Setpoint limits. The unit holds its own in registers 140 and 141; these are
// the bounds the register list gives and serve until those are read.
const (
	SetpointMin = 10.0
	SetpointMax = 30.0
)
