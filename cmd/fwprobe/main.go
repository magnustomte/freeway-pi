// SPDX-License-Identifier: GPL-3.0-or-later
//
// Copyright (C) 2026 Magnus Fonn Tømte
//
// This file is part of fwprobe. It comes with no warranty whatsoever; see
// the GNU General Public License in LICENSE for the terms it is offered under.

// Command fwprobe verifies an RS-485 connection to an Enervent EDA unit before
// anything is built on top of it.
//
// It exists to answer four questions, in order:
//
//	clock   Is the wiring right and is anything listening at all?
//	known   Do the values match what the old interface shows?
//	fc6     Does write-single-register work directly on RS-485?
//	timing  How fast may the bus be driven?
//
// and then to record what this particular unit actually has:
//
//	sweep   Which registers answer, and with what?
//
// Only fc6 writes anything, it writes one reversible value, and it refuses to
// run without --yes.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"freewaypi/internal/modbus"
	"freewaypi/internal/serial"
)

const usage = `fwprobe - verify an RS-485 link to an Enervent EDA unit

usage: fwprobe [flags] <command>

commands:
  clock     read the real-time clock block; unambiguous, so a real answer is obvious
  known     read the registers whose values can be compared against the old interface
  read      read specific registers or coils
  write     write holding registers, for testing a hypothesis about one
  fc6       test whether write-single-register (function 6) reaches the unit
  timing    measure turnaround and find the shortest safe gap between requests
  blocks    measure how turnaround grows with the number of registers read
  snapshot  time a full refresh of the mapped register space, as the poller will do it
  sweep     probe an address range and record which registers exist

flags:
`

type options struct {
	port       string
	baud       int
	slave      int
	stopBits   int
	parity     string
	timeout    time.Duration
	interFrame time.Duration
	retries    int
	verbose    bool
}

func main() {
	var o options
	flag.StringVar(&o.port, "port", "/dev/ttyUSB0", "serial device")
	flag.IntVar(&o.baud, "baud", 19200, "baud rate")
	flag.IntVar(&o.slave, "slave", 1, "Modbus unit address")
	flag.IntVar(&o.stopBits, "stopbits", 1, "stop bits, 1 or 2")
	flag.StringVar(&o.parity, "parity", "none", "parity: none, even or odd")
	flag.DurationVar(&o.timeout, "timeout", time.Second, "per-attempt timeout")
	flag.DurationVar(&o.interFrame, "gap", 20*time.Millisecond, "silence held between requests")
	flag.IntVar(&o.retries, "retries", 2, "retries after a transport failure")
	flag.BoolVar(&o.verbose, "v", false, "print every frame on the wire")

	flag.Usage = func() {
		fmt.Fprint(os.Stderr, usage)
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}

	if err := run(&o, flag.Arg(0), flag.Args()[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "fwprobe: %v\n", err)
		os.Exit(1)
	}
}

func run(o *options, cmd string, args []string) error {
	c, closePort, err := open(o)
	if err != nil {
		return err
	}
	defer closePort()

	switch cmd {
	case "clock":
		return cmdClock(c)
	case "known":
		return cmdKnown(c)
	case "read":
		return cmdRead(c, args)
	case "write":
		return cmdWrite(c, args)
	case "fc6":
		return cmdFC6(c, args)
	case "timing":
		return cmdTiming(c, o, args)
	case "snapshot":
		return cmdSnapshot(c, args)
	case "blocks":
		return cmdBlocks(c, args)
	case "sweep":
		return cmdSweep(c, args)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func open(o *options) (*modbus.Client, func(), error) {
	cfg := serial.DefaultConfig(o.port)
	cfg.Baud = o.baud
	cfg.StopBits = o.stopBits
	switch strings.ToLower(o.parity) {
	case "none", "n":
		cfg.Parity = serial.ParityNone
	case "even", "e":
		cfg.Parity = serial.ParityEven
	case "odd", "o":
		cfg.Parity = serial.ParityOdd
	default:
		return nil, nil, fmt.Errorf("unknown parity %q", o.parity)
	}

	p, err := serial.Open(cfg)
	if err != nil {
		return nil, nil, err
	}

	c := modbus.NewClient(p, byte(o.slave))
	c.Timeout = o.timeout
	c.InterFrame = o.interFrame
	c.Retries = o.retries
	if o.verbose {
		c.OnExchange = func(ex modbus.Exchange) {
			status := "ok"
			if ex.Err != nil {
				status = ex.Err.Error()
			}
			fmt.Fprintf(os.Stderr, "  -> % x\n  <- % x  (%s, %s)\n",
				ex.Request, ex.Response, ex.Turnaround.Round(time.Millisecond), status)
		}
	}
	return c, func() { p.Close() }, nil
}

// --- clock -----------------------------------------------------------------

func cmdClock(c *modbus.Client) error {
	// Seconds through weekday in one block, so the values are a snapshot
	// rather than seven readings taken seconds apart.
	regs, err := c.ReadHolding(37, 7)
	if err != nil {
		return fmt.Errorf("reading the clock block at 37: %w%s", err, wiringHint(err))
	}
	sec, min, hour := regs[0], regs[1], regs[2]
	day, month, year, weekday := regs[3], regs[4], regs[5], regs[6]

	days := []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	name := "?"
	if int(weekday) < len(days) {
		name = days[weekday]
	}

	fmt.Printf("The unit answered.\n\n")
	fmt.Printf("  clock    %02d:%02d:%02d\n", hour, min, sec)
	fmt.Printf("  date     %s %d.%d.%d\n", name, day, month, 2000+int(year))
	fmt.Printf("  raw      %v\n\n", regs)
	fmt.Printf("Compare that with the unit's own panel. If it matches, the wiring,\n")
	fmt.Printf("the baud rate and the unit address are all correct.\n")
	return nil
}

func wiringHint(err error) string {
	if err == modbus.ErrTimeout || strings.Contains(err.Error(), "no answer") {
		return "\n\nSilence usually means Data + and Data - are swapped. Try them the other\n" +
			"way round. Check too that the Freeway adapter is unplugged: RS-485 allows\n" +
			"exactly one master, and Freeway will not share."
	}
	return ""
}

// --- known -----------------------------------------------------------------

// The few registers needed to confirm the link against the old interface. The
// authoritative register list is Enervent's own document and is not
// duplicated here; these are only labels for a diagnostic.
var knownHolding = []struct {
	addr  uint16
	label string
	scale int
}{
	{44, "status bit field", 1},
	{45, "temperature control step", 1},
	{50, "fan level in effect (%)", 1},
	{53, "fan level set on panel (%)", 1},
	{56, "overpressure duration in use (min)", 1},
	{57, "overpressure duration, second copy (min)", 1},
	{135, "temperature setpoint (C)", 10},
	{164, "cooling blocked below (C)", 10},
	{196, "heating blocked above (C)", 10},
	{538, "service interval (days)", 1},
	{599, "software version", 1},
}

var knownCoils = []struct {
	addr  uint16
	label string
}{
	{0, "stop"}, {1, "away"}, {2, "long away"}, {3, "overpressure"},
	{6, "max heating"}, {7, "max cooling"}, {10, "manual boost"},
	{16, "fan type: EC when 1"}, {28, "cooling in operation"},
	{30, "heat recovery running"}, {32, "heating in operation"},
	{42, "B alarm active"}, {49, "service reminder enabled"},
	{52, "cooling allowed"}, {54, "heating allowed"},
}

func cmdKnown(c *modbus.Client) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "REGISTER\tRAW\tVALUE\tMEANING")
	for _, r := range knownHolding {
		regs, err := c.ReadHolding(r.addr, 1)
		if err != nil {
			fmt.Fprintf(w, "HR%d\t-\t%v\t%s\n", r.addr, err, r.label)
			continue
		}
		raw := regs[0]
		value := fmt.Sprintf("%d", raw)
		if r.scale == 10 {
			value = fmt.Sprintf("%.1f", float64(int16(raw))/10)
		}
		fmt.Fprintf(w, "HR%d\t%d\t%s\t%s\n", r.addr, raw, value, r.label)
	}
	fmt.Fprintln(w, "\tCOIL\tSTATE\tMEANING")
	for _, r := range knownCoils {
		bits, err := c.ReadCoils(r.addr, 1)
		if err != nil {
			fmt.Fprintf(w, "\tC%d\t%v\t%s\n", r.addr, err, r.label)
			continue
		}
		state := "0"
		if bits[0] {
			state = "1"
		}
		fmt.Fprintf(w, "\tC%d\t%s\t%s\n", r.addr, state, r.label)
	}
	w.Flush()

	fmt.Printf("\nCompare these against the old web interface before trusting anything\n")
	fmt.Printf("built on top. Readings are safe; nothing here writes.\n")
	return nil
}

// --- read ------------------------------------------------------------------

func cmdRead(c *modbus.Client, args []string) error {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	hreg := fs.Int("hreg", -1, "holding register address")
	coil := fs.Int("coil", -1, "coil address")
	count := fs.Int("count", 1, "how many consecutive addresses")
	fs.Parse(args)

	switch {
	case *hreg >= 0:
		regs, err := c.ReadHolding(uint16(*hreg), uint16(*count))
		if err != nil {
			return err
		}
		for i, v := range regs {
			fmt.Printf("HR%d = %d  (signed %d, /10 %.1f)\n", *hreg+i, v, int16(v), float64(int16(v))/10)
		}
	case *coil >= 0:
		bits, err := c.ReadCoils(uint16(*coil), uint16(*count))
		if err != nil {
			return err
		}
		for i, v := range bits {
			n := 0
			if v {
				n = 1
			}
			fmt.Printf("C%d = %d\n", *coil+i, n)
		}
	default:
		return fmt.Errorf("give either -hreg or -coil")
	}
	return nil
}

// --- fc6 -------------------------------------------------------------------

// testRegister is holding register 57, the second copy of the overpressure
// duration. The unit does not act on it, so changing it by one and putting it
// back is the safest write available.
// defaultTestRegister is the overpressure duration: a value with no effect
// until overpressure runs, and one the unit does not rewrite by itself.
const defaultTestRegister = 57

// cmdWrite writes consecutive holding registers and reads them back.
//
// Unlike fc6 it does not put anything back: it exists for testing what a write
// actually does to the unit, which is the question fc6 cannot answer because it
// undoes itself. Everything about it is deliberate friction — it names no
// default register, takes the values explicitly, and refuses without --yes.
func cmdWrite(c *modbus.Client, args []string) error {
	fs := flag.NewFlagSet("write", flag.ExitOnError)
	addr := fs.Int("hreg", -1, "first holding register address")
	yes := fs.Bool("yes", false, "confirm the write")
	fs.Parse(args)

	if *addr < 0 {
		return fmt.Errorf("give -hreg")
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("give the values to write after the flags")
	}
	values := make([]uint16, 0, len(rest))
	for _, a := range rest {
		v, err := strconv.Atoi(a)
		if err != nil || v < 0 || v > 65535 {
			return fmt.Errorf("%q is not a register value", a)
		}
		values = append(values, uint16(v))
	}
	if !*yes {
		return fmt.Errorf("this writes %v to HR%d and does not put it back; rerun with --yes",
			values, *addr)
	}

	before, err := c.ReadHolding(uint16(*addr), uint16(len(values)))
	if err != nil {
		return fmt.Errorf("reading HR%d first: %w", *addr, err)
	}
	fmt.Printf("HR%d..%d before: %v\n", *addr, *addr+len(values)-1, before)

	if err := c.WriteRegisters(uint16(*addr), values); err != nil {
		return fmt.Errorf("writing: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	after, err := c.ReadHolding(uint16(*addr), uint16(len(values)))
	if err != nil {
		return fmt.Errorf("reading back: %w", err)
	}
	fmt.Printf("HR%d..%d after:  %v\n", *addr, *addr+len(values)-1, after)
	return nil
}

func cmdFC6(c *modbus.Client, args []string) error {
	// Parsed here rather than as a global flag: Go stops looking for flags at
	// the first bare argument, so a global --yes would have to come before the
	// subcommand, which is exactly the sort of trap that gets a confirmation
	// typed twice and then ignored.
	fs := flag.NewFlagSet("fc6", flag.ExitOnError)
	yes := fs.Bool("yes", false, "confirm the one reversible write this test makes")
	// Which register, because the answer is per register: the list's R/W
	// column has been wrong in both directions on the unit measured, so "is this one
	// writable" is a question that has to be asked of each one.
	reg := fs.Uint("register", defaultTestRegister, "holding register to test")
	fs.Parse(args)
	testRegister := uint16(*reg)

	if !*yes {
		return fmt.Errorf("this test writes to holding register %d and puts it back; rerun with --yes", testRegister)
	}

	before, err := c.ReadHolding(testRegister, 1)
	if err != nil {
		return fmt.Errorf("reading HR%d first: %w", testRegister, err)
	}
	original := before[0]
	probe := original + 1
	fmt.Printf("HR%d currently reads %d. Writing %d and reading back.\n\n", testRegister, original, probe)

	fc6Works := false
	if err := c.WriteSingleRegister(testRegister, probe); err != nil {
		fmt.Printf("  function 6  request refused: %v\n", err)
	} else {
		time.Sleep(500 * time.Millisecond)
		after, err := c.ReadHolding(testRegister, 1)
		if err != nil {
			return fmt.Errorf("reading back after function 6: %w", err)
		}
		if after[0] == probe {
			fc6Works = true
			fmt.Printf("  function 6  acknowledged and APPLIED   (reads %d)\n", after[0])
		} else {
			fmt.Printf("  function 6  acknowledged but IGNORED  (still reads %d)\n", after[0])
		}
	}

	// Whether or not 6 worked, confirm 16 does, since that is what the Homey
	// app uses and what the gateway must not break.
	fc16Works := false
	if err := c.WriteRegisters(testRegister, []uint16{probe}); err != nil {
		fmt.Printf("  function 16 request refused: %v\n", err)
	} else {
		time.Sleep(500 * time.Millisecond)
		after, err := c.ReadHolding(testRegister, 1)
		if err != nil {
			return fmt.Errorf("reading back after function 16: %w", err)
		}
		if after[0] == probe {
			fc16Works = true
			fmt.Printf("  function 16 acknowledged and APPLIED   (reads %d)\n", after[0])
		} else {
			fmt.Printf("  function 16 acknowledged but IGNORED  (still reads %d)\n", after[0])
		}
	}

	// Put it back, with whichever code the unit honours.
	restore := c.WriteRegisters
	if !fc16Works && fc6Works {
		restore = func(addr uint16, v []uint16) error { return c.WriteSingleRegister(addr, v[0]) }
	}
	if err := restore(testRegister, []uint16{original}); err != nil {
		return fmt.Errorf("RESTORE FAILED, HR%d may still read %d instead of %d: %w",
			testRegister, probe, original, err)
	}
	time.Sleep(500 * time.Millisecond)
	final, err := c.ReadHolding(testRegister, 1)
	if err == nil && final[0] != original {
		return fmt.Errorf("RESTORE FAILED, HR%d reads %d, was %d", testRegister, final[0], original)
	}
	fmt.Printf("  restored    HR%d is back to %d\n\n", testRegister, original)

	switch {
	case fc6Works:
		fmt.Printf("Verdict: function 6 reaches the unit. Writes that were not seen to\n")
		fmt.Printf("arrive through the original adapter were not refused by the board. The\n")
		fmt.Printf("gateway can pass all four write codes through unchanged.\n")
	case fc16Works:
		fmt.Printf("Verdict: function 6 does NOT reach the unit even directly on RS-485, so\n")
		fmt.Printf("the board does not accept it. The gateway must translate 5 and 6 into 15\n")
		fmt.Printf("and 16 on the way out, or clients will fail silently.\n")
	default:
		fmt.Printf("Verdict: neither write code applied. Something else is wrong; do not\n")
		fmt.Printf("build on this until it is understood.\n")
	}
	return nil
}

// --- timing ----------------------------------------------------------------

func cmdTiming(c *modbus.Client, o *options, args []string) error {
	fs := flag.NewFlagSet("timing", flag.ExitOnError)
	samples := fs.Int("samples", 100, "reads used to measure turnaround")
	attempts := fs.Int("attempts", 30, "reads per gap in the ladder; 0 skips the ladder")
	fs.Parse(args)
	if *samples < 1 {
		*samples = 1
	}

	fmt.Printf("Measuring turnaround over %d reads of the clock block.\n\n", *samples)
	var good []time.Duration
	fails := map[string]int{}
	c.OnExchange = func(ex modbus.Exchange) {
		if ex.Err == nil {
			good = append(good, ex.Turnaround)
		} else {
			fails[classify(ex.Err)]++
		}
	}
	for i := 0; i < *samples; i++ {
		if _, err := c.ReadHolding(37, 7); err != nil {
			return fmt.Errorf("read %d of %d: %w", i+1, *samples, err)
		}
	}
	c.OnExchange = nil
	report("turnaround", good)
	reportFailures(fails, len(good))

	// The ladder is run in full rather than stopped at the first failure. If
	// the failures are spread evenly across every gap then the gap is not the
	// cause, and stopping early would hide exactly that.
	if *attempts < 1 {
		return nil
	}
	fmt.Printf("\nGap ladder: %d requests at each gap, retries off.\n\n", *attempts)

	gaps := []time.Duration{
		0, 2 * time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond,
		20 * time.Millisecond, 35 * time.Millisecond, 50 * time.Millisecond,
		75 * time.Millisecond, 100 * time.Millisecond,
	}
	savedRetries := c.Retries
	c.Retries = 0

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "GAP\tOK\tFAILED\tRATE\tKINDS")
	cleanest := time.Duration(-1)
	totalFail, totalReq := 0, 0
	for _, gap := range gaps {
		c.InterFrame = gap
		kinds := map[string]int{}
		ok := 0
		for i := 0; i < *attempts; i++ {
			if _, err := c.ReadHolding(37, 7); err != nil {
				kinds[classify(err)]++
			} else {
				ok++
			}
		}
		failed := *attempts - ok
		totalFail += failed
		totalReq += *attempts
		if failed == 0 && cleanest < 0 {
			cleanest = gap
		}
		fmt.Fprintf(w, "%v\t%d\t%d\t%.0f%%\t%s\n", gap, ok, failed,
			100*float64(failed)/float64(*attempts), describeKinds(kinds))
	}
	w.Flush()
	c.InterFrame = o.interFrame
	c.Retries = savedRetries

	fmt.Printf("\nOverall single-attempt failure rate: %.1f%% over %d requests.\n",
		100*float64(totalFail)/float64(totalReq), totalReq)
	switch {
	case cleanest == 0:
		fmt.Printf("No gap was needed: even back-to-back requests were clean.\n")
	case cleanest > 0:
		fmt.Printf("Smallest clean gap: %v. Use a margin above it; the bus will be\n", cleanest)
		fmt.Printf("busier in service than it is now.\n")
	default:
		fmt.Printf("NO gap was clean, including the longest tested. The failures are not\n")
		fmt.Printf("caused by requests arriving too quickly, so a larger gap will not fix\n")
		fmt.Printf("them. Look at the failure kinds above and at the FTDI latency timer.\n")
	}
	return nil
}

// classify buckets an error so the ladder can show whether failures share a
// cause. A timeout and a bad CRC mean very different things: silence suggests
// the unit never answered, a bad CRC suggests it did and the line mangled it.
func classify(err error) string {
	switch {
	case errors.Is(err, modbus.ErrTimeout):
		return "timeout"
	case strings.Contains(err.Error(), "bad CRC"):
		return "crc"
	case strings.Contains(err.Error(), "short frame"), strings.Contains(err.Error(), "of "):
		return "truncated"
	default:
		return "other"
	}
}

func describeKinds(kinds map[string]int) string {
	if len(kinds) == 0 {
		return "-"
	}
	names := make([]string, 0, len(kinds))
	for k := range kinds {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s %d", n, kinds[n]))
	}
	return strings.Join(parts, ", ")
}

func reportFailures(fails map[string]int, good int) {
	total := good
	for _, n := range fails {
		total += n
	}
	if len(fails) == 0 {
		fmt.Printf("    no failures in %d attempts\n", total)
		return
	}
	failed := total - good
	fmt.Printf("    %d of %d attempts failed (%.1f%%): %s\n",
		failed, total, 100*float64(failed)/float64(total), describeKinds(fails))
	fmt.Printf("    note: these are single attempts; the client normally retries\n")
}

func report(name string, d []time.Duration) {
	if len(d) == 0 {
		fmt.Printf("  %s: no samples\n", name)
		return
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	pct := func(p float64) time.Duration { return d[int(float64(len(d)-1)*p)] }
	fmt.Printf("  %s over %d samples\n", name, len(d))
	fmt.Printf("    min %v   median %v   p95 %v   max %v\n",
		d[0].Round(100*time.Microsecond),
		pct(0.50).Round(100*time.Microsecond),
		pct(0.95).Round(100*time.Microsecond),
		d[len(d)-1].Round(100*time.Microsecond))
	histogram(d)
}

// histogram matters more than the percentiles here. A tight cluster means the
// unit answers on demand; an even spread from near zero up to some ceiling
// means requests are waiting for a periodic service cycle, and that ceiling is
// the cycle length. The two call for completely different queue designs.
func histogram(d []time.Duration) {
	const buckets = 12
	lo, hi := d[0], d[len(d)-1]
	span := hi - lo
	if span <= 0 {
		return
	}
	counts := make([]int, buckets)
	for _, v := range d {
		b := int(int64(buckets) * int64(v-lo) / int64(span))
		if b >= buckets {
			b = buckets - 1
		}
		counts[b]++
	}
	max := 0
	for _, n := range counts {
		if n > max {
			max = n
		}
	}
	fmt.Println()
	for i, n := range counts {
		from := lo + time.Duration(int64(i)*int64(span)/buckets)
		bar := strings.Repeat("#", 40*n/max)
		fmt.Printf("    %6v  %-40s %d\n", from.Round(time.Millisecond), bar, n)
	}
}

// cmdSnapshot reads the unit's whole mapped register space the way the poller
// will, in the largest blocks Modbus allows, and reports what one full refresh
// costs. That figure is the bus budget: everything left over belongs to the
// Modbus TCP clients.
func cmdSnapshot(c *modbus.Client, args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	last := fs.Int("last", 737, "highest holding register to include")
	coils := fs.Int("coils", 79, "highest coil to include")
	reps := fs.Int("reps", 5, "how many full snapshots to time")
	block := fs.Int("block", 0, "registers per request, at most 125; 0 compares a range of sizes")
	fs.Parse(args)

	if *block > 0 {
		return snapshotOnce(c, *last, *coils, *reps, *block)
	}
	// The per-request medians from "blocks" are unstable, because a third of
	// reads land in the unit's slow mode and skew a 20-sample median. Timing
	// the whole refresh at each size measures the thing that actually matters
	// and averages that noise out.
	for _, b := range []int{5, 10, 20, 40, 60, 120, 125} {
		if err := snapshotOnce(c, *last, *coils, *reps, b); err != nil {
			return err
		}
	}
	return nil
}

func snapshotOnce(c *modbus.Client, last, coils, reps, maxRegs int) error {

	var totals []time.Duration
	requests := 0
	for r := 0; r < reps; r++ {
		start := time.Now()
		requests = 0
		for addr := 0; addr <= last; addr += maxRegs {
			n := maxRegs
			if addr+n > last+1 {
				n = last + 1 - addr
			}
			if _, err := c.ReadHolding(uint16(addr), uint16(n)); err != nil {
				return fmt.Errorf("holding %d..%d: %w", addr, addr+n-1, err)
			}
			requests++
		}
		if _, err := c.ReadCoils(0, uint16(coils+1)); err != nil {
			return fmt.Errorf("coils: %w", err)
		}
		requests++
		totals = append(totals, time.Since(start))
	}

	sort.Slice(totals, func(i, j int) bool { return totals[i] < totals[j] })
	median := totals[len(totals)/2]
	fmt.Printf("  block %3d  %2d requests  min %6v  median %6v  max %6v  duty %.0f%%\n",
		maxRegs, requests,
		totals[0].Round(time.Millisecond),
		median.Round(time.Millisecond),
		totals[len(totals)-1].Round(time.Millisecond),
		100*median.Seconds()/10)
	return nil
}

// cmdBlocks measures how turnaround grows with the number of registers asked
// for. If a 120-register read costs little more than a 1-register read, the
// poller should read the map in a few large blocks rather than register by
// register, and the difference is the whole budget of the bus.
func cmdBlocks(c *modbus.Client, args []string) error {
	fs := flag.NewFlagSet("blocks", flag.ExitOnError)
	reps := fs.Int("reps", 20, "reads per block size")
	fs.Parse(args)

	if *reps < 1 {
		*reps = 1
	}
	sizes := []uint16{1, 2, 5, 10, 20, 40, 80, 120}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "REGISTERS\tMEDIAN\tMIN\tMAX\tREGISTERS/S\tFAILED")
	for _, n := range sizes {
		var d []time.Duration
		failed := 0
		c.OnExchange = func(ex modbus.Exchange) {
			if ex.Err == nil {
				d = append(d, ex.Turnaround)
			} else {
				failed++
			}
		}
		for i := 0; i < *reps; i++ {
			if _, err := c.ReadHolding(0, n); err != nil {
				return fmt.Errorf("reading %d registers: %w", n, err)
			}
		}
		c.OnExchange = nil
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		median := d[len(d)/2]
		perSec := float64(n) / median.Seconds()
		fmt.Fprintf(w, "%d\t%v\t%v\t%v\t%.0f\t%d\n", n,
			median.Round(time.Millisecond), d[0].Round(time.Millisecond),
			d[len(d)-1].Round(time.Millisecond), perSec, failed)
	}
	w.Flush()
	fmt.Printf("\nIf registers/s climbs steeply with block size, the cost is per request\n")
	fmt.Printf("and not per register, and the poller should read in large blocks.\n")
	return nil
}

// --- sweep -----------------------------------------------------------------

func cmdSweep(c *modbus.Client, args []string) error {
	fs := flag.NewFlagSet("sweep", flag.ExitOnError)
	kind := fs.String("kind", "hreg", "hreg or coil")
	from := fs.Int("from", 0, "first address")
	to := fs.Int("to", 800, "last address, inclusive")
	out := fs.String("out", "", "write JSON to this file as well")
	fs.Parse(args)

	fmt.Fprintf(os.Stderr, "Sweeping %s %d..%d. Reading only; nothing is written.\n", *kind, *from, *to)

	var entries []sweepEntry
	counts := map[string]int{}
	byStatus := map[string][]int{}

	for addr := *from; addr <= *to; addr++ {
		e := sweepEntry{Address: addr}
		var err error
		if *kind == "coil" {
			var bits []bool
			bits, err = c.ReadCoils(uint16(addr), 1)
			if err == nil && bits[0] {
				e.Value = 1
			}
		} else {
			var regs []uint16
			regs, err = c.ReadHolding(uint16(addr), 1)
			if err == nil {
				e.Value = int(regs[0])
			}
		}

		switch {
		case err == nil && *kind != "coil" && e.Value == 0xFFFF:
			// The board answers its whole address space without complaint and
			// returns 0xFFFF where nothing is mapped. Long runs of it are holes
			// in the register map, not readings. A lone 0xFFFF may still be a
			// genuine -1, so this is a hint, not a verdict.
			e.Status = "unmapped"
		case err == nil:
			e.Status = "ok"
		case modbus.IsIllegalDataAddress(err):
			e.Status = "absent"
		default:
			e.Status = "error"
			e.Error = err.Error()
		}
		counts[e.Status]++
		byStatus[e.Status] = append(byStatus[e.Status], addr)
		entries = append(entries, e)

		if (addr-*from)%100 == 0 && addr > *from {
			fmt.Fprintf(os.Stderr, "  at %d\n", addr)
		}
	}

	fmt.Printf("\n%d addresses read.\n\n", len(entries))
	for _, st := range []string{"ok", "unmapped", "absent", "error"} {
		if counts[st] == 0 {
			continue
		}
		fmt.Printf("  %-9s %4d   %s\n", st, counts[st], compressRanges(byStatus[st]))
	}
	fmt.Printf("\n  ok        answered with something other than the unmapped marker\n")
	fmt.Printf("  unmapped  answered 0xFFFF; almost certainly a hole in the map\n")
	fmt.Printf("  absent    refused with illegal data address\n")
	fmt.Printf("  error     no usable answer; rerun these with -v before concluding\n")

	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		fmt.Fprintln(f, "[")
		for i, e := range entries {
			comma := ","
			if i == len(entries)-1 {
				comma = ""
			}
			fmt.Fprintf(f, "  {\"address\": %d, \"status\": %q, \"value\": %d, \"error\": %q}%s\n",
				e.Address, e.Status, e.Value, e.Error, comma)
		}
		fmt.Fprintln(f, "]")
		fmt.Printf("\nProfile written to %s.\n", *out)
	}
	return nil
}

type sweepEntry struct {
	Address int
	Status  string
	Value   int
	Error   string
}

// compressRanges renders a sorted address list as "1-3, 7, 20-99", which is the
// only way a 100-entry list of holes is readable.
func compressRanges(xs []int) string {
	if len(xs) == 0 {
		return "-"
	}
	var parts []string
	start, prev := xs[0], xs[0]
	flush := func() {
		if start == prev {
			parts = append(parts, fmt.Sprintf("%d", start))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", start, prev))
		}
	}
	for _, x := range xs[1:] {
		if x == prev+1 {
			prev = x
			continue
		}
		flush()
		start, prev = x, x
	}
	flush()
	s := strings.Join(parts, ", ")
	if len(s) > 200 {
		s = s[:197] + "..."
	}
	return s
}
