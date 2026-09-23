// Command demo serves the web interface against a simulated ventilation unit.
//
// It exists so the interface can be looked at — and photographed for the
// README — without a Raspberry Pi, an RS-485 cable or anybody's house. Every
// number below is invented.
package main

import (
	"flag"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"freewaypi/internal/auth"
	"freewaypi/internal/bus"
	"freewaypi/internal/control"
	"freewaypi/internal/eda"
	"freewaypi/internal/modbus"
	"freewaypi/internal/modbustest"
	"freewaypi/internal/web"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:8080", "address to serve on")
	flag.Parse()

	u := modbustest.NewUnit(1, bus.LastHolding+1, bus.LastCoil+1)
	load(u)

	c := modbus.NewClient(u, 1)
	c.InterFrame = 0
	c.Timeout = 200 * time.Millisecond
	b := bus.New(c, bus.Options{})
	defer b.Close()
	p := bus.NewPoller(b, bus.PollerOptions{Interval: 10 * time.Second})
	defer p.Close()

	unit := control.New(b, p, 10*time.Second, "Ventilation")
	srv := web.New(unit, p, web.Options{
		Auth:    auth.New(auth.Config{}),
		Logger:  slog.New(slog.NewTextHandler(os.Stderr, nil)),
		Version: "demo",
	})

	log.Printf("demo interface on http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, srv))
}

// load fills the simulated board with a plausible winter afternoon.
func load(u *modbustest.Unit) {
	set := func(addr int, v int) { u.Holding[addr] = uint16(v) }

	// Temperatures, in tenths of a degree as the board keeps them.
	set(eda.HRFreshAir, 40)     // outdoors
	set(eda.HRSupplyAfter, 175) // after the exchanger
	set(eda.HRSupply, 195)      // into the rooms
	set(eda.HRWaste, 90)        // thrown out
	set(eda.HRExtract, 215)     // out of the rooms
	set(eda.HRPanelTemp, 210)
	set(eda.HRRoomTempMean, 210)
	set(eda.HRHumidity, 38)

	set(eda.HRSetpoint, 210) // tenths, unlike the limits below
	set(eda.HRSetpointMin, 18)
	set(eda.HRSetpointMax, 26)
	set(eda.HRFanSetpoint, 55)
	set(eda.HRFanActual, 55)

	set(eda.HRCascadeI, 145) // recovery full, heating by the remainder
	set(eda.HRControlStep, 4)
	set(eda.HRSupplyTarget, 210)
	set(eda.HRSupplyMin, 130)
	set(eda.HRSupplyMax, 300)
	set(eda.HREfficiencySupply, 78)
	set(eda.HREfficiencyExtract, 81)

	set(eda.HRSoftwareVersion, 600)
	set(eda.HRMachineFamily, 3)
	set(eda.HRSerialNumber, 0)

	set(eda.HROverpressureInUse, 20)
	set(eda.HRSupplyOverpressure, 50)
	set(eda.HRExtractOverpressure, 30)
	set(eda.HRBoostMinutes, 30)
	set(eda.HRBoostLevel, 90)

	set(eda.HRServiceInterval, 120)
	set(eda.HRDaysSinceService, 34)

	now := time.Now()
	set(eda.HRHours, now.Hour())
	set(eda.HRMinutes, now.Minute())
	set(eda.HRDay, now.Day())
	set(eda.HRMonth, int(now.Month()))
	set(eda.HRYear, now.Year()%100)

	// The state word carries modes and faults; heating and recovery are coils.
	set(eda.HRState, 0)
	u.Coils[eda.CoilECFans] = true
	u.Coils[eda.CoilHeatRecovery] = true
	u.Coils[eda.CoilHeating] = true
}
