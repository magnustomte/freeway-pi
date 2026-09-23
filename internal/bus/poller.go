package bus

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"freewaypi/internal/modbus"
)

// Measurement established the shape of the unit and these constants come straight
// from it. Reading the whole map in seven requests of 125 registers takes about
// 1.5 s; at five registers per request the same refresh takes 17 s, because the
// cost is per request and not per register.
const (
	// MaxBlock is the protocol's limit for reading holding registers.
	MaxBlock = 125
	// LastHolding is the highest address the unit measured maps. Above it the board
	// answers 0xFFFF up to 799 and refuses from 800.
	LastHolding = 737
	// LastCoil is the highest coil the board accepts; 80 and above are refused.
	LastCoil = 79
	// DefaultInterval matches holding register 726, the unit's own refresh
	// interval for its measurement registers. Polling faster returns the same
	// values, not fresher ones.
	DefaultInterval = 10 * time.Second
)

// Snapshot is one pass over the unit's register map.
//
// It is not an instant in time, and cannot be: the reads are spread over about
// a second and a half, deliberately, so that a write from the web interface can
// slot in between blocks rather than waiting for the whole pass. That costs
// nothing in fidelity, because the unit is not a coherent source either — its
// status bit field is known to lag its coils by the better part of ten seconds.
type Snapshot struct {
	Started  time.Time
	Finished time.Time
	Holding  []uint16
	Coils    []bool
	// Partial records which blocks failed, so a caller can tell a stale value
	// from a fresh one instead of silently trusting a gap.
	Failed []BlockError
}

// BlockError is one failed read within a pass.
type BlockError struct {
	Kind  string // "holding" or "coil"
	From  int
	Count int
	Err   error
}

func (e BlockError) Error() string {
	return fmt.Sprintf("%s %d..%d: %v", e.Kind, e.From, e.From+e.Count-1, e.Err)
}

// Complete reports whether every block in the pass was read.
func (s *Snapshot) Complete() bool { return len(s.Failed) == 0 }

// Age is how long ago the pass finished.
func (s *Snapshot) Age() time.Duration { return time.Since(s.Finished) }

// Poller keeps a recent copy of the whole register map.
type Poller struct {
	bus      *Bus
	interval time.Duration

	current atomic.Pointer[Snapshot]

	mu       sync.Mutex
	passes   uint64
	failures uint64
	lastErr  error

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// PollerOptions configures a Poller.
type PollerOptions struct {
	// Interval between the start of one pass and the next. A pass that
	// overruns simply delays the next one; passes never overlap, because two
	// passes would only queue behind each other on a bus that serves one
	// request at a time.
	Interval time.Duration
}

// NewPoller starts polling. Call Close to stop.
func NewPoller(b *Bus, opts PollerOptions) *Poller {
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	p := &Poller{
		bus:      b,
		interval: opts.Interval,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go p.loop()
	return p
}

func (p *Poller) loop() {
	defer close(p.done)
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
		}
		p.Refresh(context.Background())
		t.Reset(nextDelay(p.interval, time.Now()))
	}
}

// nextDelay is how long to wait before starting the next pass, aligning passes
// to wall-clock multiples of the interval.
//
// Two bugs live here and only alignment fixes both. Waiting a whole interval
// after the pass finished makes the real period the interval plus the pass,
// ten seconds plus about one and a half on the unit measured. Subtracting the
// pass time fixes the period but not the phase: the passes then drift, and each
// time the drift carries one across a boundary of the grid the history is
// bucketed on, a bucket is skipped and the chart gets a hole.
//
// Anchoring to the grid gives exactly one pass per bucket and no accumulated
// drift. A pass that overruns does not pile the next ones up: the loop is
// sequential, so the next simply starts at the following boundary.
func nextDelay(interval time.Duration, now time.Time) time.Duration {
	if interval <= 0 {
		return DefaultInterval
	}
	d := now.Truncate(interval).Add(interval).Sub(now)
	// A boundary that is upon us already would mean starting a pass with no
	// pause at all, so the nearly-zero case waits for the one after.
	if d < interval/10 {
		d += interval
	}
	return d
}

// Refresh takes one pass and publishes it. It is exported so a write can force
// an immediate re-read rather than leave the interface showing the old value
// for up to a full interval.
func (p *Poller) Refresh(ctx context.Context) *Snapshot {
	s := &Snapshot{
		Started: time.Now(),
		Holding: make([]uint16, LastHolding+1),
		Coils:   make([]bool, LastCoil+1),
	}

	// One bus job per block rather than one for the whole pass. A single job
	// holding the bus for a second and a half would put exactly that much
	// latency in front of anyone pressing a button.
	for addr := 0; addr <= LastHolding; addr += MaxBlock {
		n := MaxBlock
		if addr+n > LastHolding+1 {
			n = LastHolding + 1 - addr
		}
		from, count := addr, n
		err := p.bus.Do(ctx, PriorityPoll, func(c *modbus.Client) error {
			regs, err := c.ReadHolding(uint16(from), uint16(count))
			if err != nil {
				return err
			}
			copy(s.Holding[from:], regs)
			return nil
		})
		if err != nil {
			s.Failed = append(s.Failed, BlockError{Kind: "holding", From: from, Count: count, Err: err})
		}
	}

	err := p.bus.Do(ctx, PriorityPoll, func(c *modbus.Client) error {
		bits, err := c.ReadCoils(0, LastCoil+1)
		if err != nil {
			return err
		}
		copy(s.Coils, bits)
		return nil
	})
	if err != nil {
		s.Failed = append(s.Failed, BlockError{Kind: "coil", From: 0, Count: LastCoil + 1, Err: err})
	}

	s.Finished = time.Now()

	p.mu.Lock()
	p.passes++
	if !s.Complete() {
		p.failures++
		p.lastErr = s.Failed[0]
	}
	p.mu.Unlock()

	// A pass that failed entirely is not published: an interface showing the
	// last known values with an honest age is more useful than one showing
	// zeroes. Partial passes are published, with Failed saying which blocks
	// carry stale data.
	if len(s.Failed) > 0 && len(s.Failed) >= passBlocks() {
		return s
	}
	p.current.Store(s)
	return s
}

// passBlocks is the number of bus jobs one pass makes.
func passBlocks() int {
	n := (LastHolding + 1 + MaxBlock - 1) / MaxBlock
	return n + 1 // plus the coil read
}

// Current returns the most recent usable snapshot, or nil before the first one
// completes.
func (p *Poller) Current() *Snapshot { return p.current.Load() }

// PollerStats reports how the polling itself is going.
type PollerStats struct {
	Passes   uint64        `json:"passes"`
	Failures uint64        `json:"failures"`
	LastErr  error         `json:"-"`
	Interval time.Duration `json:"-"`
}

// Stats returns the poller's counters.
func (p *Poller) Stats() PollerStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return PollerStats{Passes: p.passes, Failures: p.failures, LastErr: p.lastErr, Interval: p.interval}
}

// Close stops polling.
func (p *Poller) Close() {
	p.once.Do(func() { close(p.stop) })
	<-p.done
}

// Reread updates part of the published snapshot from the unit.
//
// Used immediately after a write, so the interface shows what the unit now
// holds rather than waiting up to a full interval for the next pass. It reads
// only the addresses that were written, which is one extra request, instead of
// taking a whole fresh pass at a second and a half.
//
// The snapshot is replaced rather than modified: readers hold a pointer to it,
// and patching in place would let them see a value change under them.
func (p *Poller) Reread(ctx context.Context, prio Priority, coils bool, addr, count int) error {
	cur := p.current.Load()
	if cur == nil {
		return nil // nothing published yet; the first pass will cover it
	}
	next := &Snapshot{
		Started:  cur.Started,
		Finished: cur.Finished,
		Holding:  append([]uint16(nil), cur.Holding...),
		Coils:    append([]bool(nil), cur.Coils...),
		Failed:   cur.Failed,
	}

	err := p.bus.Do(ctx, prio, func(c *modbus.Client) error {
		if coils {
			if addr+count > len(next.Coils) {
				return fmt.Errorf("bus: coils %d..%d are outside the map", addr, addr+count-1)
			}
			bits, err := c.ReadCoils(uint16(addr), uint16(count))
			if err != nil {
				return err
			}
			copy(next.Coils[addr:], bits)
			return nil
		}
		if addr+count > len(next.Holding) {
			return fmt.Errorf("bus: holding registers %d..%d are outside the map", addr, addr+count-1)
		}
		regs, err := c.ReadHolding(uint16(addr), uint16(count))
		if err != nil {
			return err
		}
		copy(next.Holding[addr:], regs)
		return nil
	})
	if err != nil {
		return err
	}

	// Only replace the snapshot if the pass that produced it is still the
	// published one. A full pass finishing during the read has newer values
	// everywhere, and overwriting it would put the rest of the map back.
	if p.current.CompareAndSwap(cur, next) {
		return nil
	}
	return nil
}
