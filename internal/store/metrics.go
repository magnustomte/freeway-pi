package store

import "freewaypi/internal/eda"

// Metric identifies one stored series. The numbers are written into the
// database, so they are fixed once shipped: a metric that is retired leaves its
// number behind rather than letting a new one inherit somebody's history.
type Metric int

const (
	MetricTempFresh Metric = iota + 1
	MetricTempSupplyAfterRecovery
	MetricTempSupply
	MetricTempWaste
	MetricTempExtract
	MetricHumidity
	MetricSetpoint
	MetricFanSetpoint
	MetricFanActual
	MetricEfficiencySupply
	MetricEfficiencyExtract
	MetricHeating
	MetricCooling
	MetricHeatRecovery
	MetricDefrosting
	MetricControlStep
	// MetricBusErrorRate is Freeway Pi's own health, not the unit's: the share
	// of requests on the wire that had to be repeated. Kept because the live
	// figure is the last 256 attempts — about seven minutes — so a burst is
	// gone before anybody can look at it, and a connector working loose is a
	// slow rise over weeks that nothing else would show.
	MetricBusErrorRate
	// MetricUndervoltage is Freeway Pi's own power supply: 1 while the kernel
	// says the five volt rail has sagged. Kept because the events are
	// milliseconds long and come and go with whatever the fan is doing, so the
	// only way to see that they line up with anything is to put them on the
	// same time axis as everything else.
	MetricUndervoltage
	// MetricOverpressure is the unit running overpressure. Kept because it is
	// the one thing a person does to the ventilation that has a beginning and
	// an end, and because the fan curve above it is unreadable without it: a
	// change that turns out to have been overpressure is not a fault.
	MetricOverpressure
)

// Definition describes a metric for the interface.
type Definition struct {
	ID    Metric
	Name  string
	Label string
	Unit  string
	// Binary marks a series that is only ever 0 or 1, which a chart should
	// draw as a band rather than as a line wandering between two values.
	Binary bool
}

// Metrics is every series kept, in the order the interface offers them.
var Metrics = []Definition{
	{MetricTempFresh, "temp_fresh", "metric.temp_fresh", "°C", false},
	{MetricTempSupply, "temp_supply", "metric.temp_supply", "°C", false},
	{MetricTempExtract, "temp_extract", "metric.temp_extract", "°C", false},
	{MetricTempWaste, "temp_waste", "metric.temp_waste", "°C", false},
	{MetricTempSupplyAfterRecovery, "temp_supply_after_recovery", "metric.temp_supply_after_recovery", "°C", false},
	{MetricSetpoint, "setpoint", "metric.setpoint", "°C", false},
	{MetricHumidity, "humidity", "metric.humidity", "%", false},
	{MetricFanSetpoint, "fan_setpoint", "metric.fan_setpoint", "%", false},
	{MetricFanActual, "fan_actual", "metric.fan_actual", "%", false},
	{MetricEfficiencySupply, "efficiency_supply", "metric.efficiency_supply", "%", false},
	{MetricEfficiencyExtract, "efficiency_extract", "metric.efficiency_extract", "%", false},
	{MetricControlStep, "control_step", "metric.control_step", "", false},
	{MetricHeating, "heating", "metric.heating", "", true},
	{MetricCooling, "cooling", "metric.cooling", "", true},
	{MetricHeatRecovery, "heat_recovery", "metric.heat_recovery", "", true},
	{MetricDefrosting, "defrosting", "metric.defrosting", "", true},
	{MetricBusErrorRate, "bus_error_rate", "metric.bus_error_rate", "%", false},
	{MetricUndervoltage, "undervoltage", "metric.undervoltage", "", true},
	{MetricOverpressure, "overpressure", "metric.overpressure", "", true},
}

var byID = func() map[Metric]Definition {
	m := make(map[Metric]Definition, len(Metrics))
	for _, d := range Metrics {
		m[d.ID] = d
	}
	return m
}()

var byName = func() map[string]Definition {
	m := make(map[string]Definition, len(Metrics))
	for _, d := range Metrics {
		m[d.Name] = d
	}
	return m
}()

// MetricByID looks up a definition.
func MetricByID(id Metric) (Definition, bool) { d, ok := byID[id]; return d, ok }

// MetricByName looks up a definition by the name the API uses.
func MetricByName(name string) (Definition, bool) { d, ok := byName[name]; return d, ok }

// SamplesFrom turns a decoded state into the readings to store.
//
// A stale state produces nothing. Writing the last known values again under a
// current timestamp would draw a flat line through an outage, which is exactly
// the shape that hides one.
func SamplesFrom(st *eda.State) []Sample {
	if st == nil || st.Stale {
		return nil
	}
	b := func(v bool) float64 {
		if v {
			return 1
		}
		return 0
	}
	return []Sample{
		{MetricTempFresh, st.Temperatures.FreshAir},
		{MetricTempSupplyAfterRecovery, st.Temperatures.SupplyAfterRecovery},
		{MetricTempSupply, st.Temperatures.Supply},
		{MetricTempWaste, st.Temperatures.Waste},
		{MetricTempExtract, st.Temperatures.Extract},
		{MetricHumidity, float64(st.Humidity)},
		{MetricSetpoint, st.Setpoint},
		{MetricFanSetpoint, float64(st.FanSetpoint)},
		{MetricFanActual, float64(st.FanActual)},
		{MetricEfficiencySupply, float64(st.Efficiency.Supply)},
		{MetricEfficiencyExtract, float64(st.Efficiency.Extract)},
		{MetricControlStep, float64(st.ControlStep)},
		{MetricHeating, b(st.Heating)},
		{MetricCooling, b(st.Cooling)},
		{MetricHeatRecovery, b(st.HeatRecovery)},
		{MetricDefrosting, b(st.Defrosting)},
		{MetricOverpressure, b(st.Overpressure)},
		// As a percentage, because that is how it is read and talked about.
		{MetricBusErrorRate, st.Link.ErrorRate * 100},
	}
}
